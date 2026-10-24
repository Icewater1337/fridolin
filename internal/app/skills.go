package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/skill"
	"github.com/Icewater1337/fridolin/internal/store"
)

// A skill's policy is what its frontmatter declares under
// metadata.fridolin, but what counts is the policy the owner approved,
// as with brain.json: a brain can write its skills, and a skill it wrote
// or changed must not give its runs more than the owner agreed to. The
// approved policies live in bridge.db (skill_approvals), by brain and
// file. A skill's status:
//
//	approved  its declared policy is the one approved
//	changed   it declares another one; the approved one keeps applying
//	new       none approved: read, and its triggers count for nothing
//	starter   as the bridge's starter skill declares it: applies as declared
//	legacy    no frontmatter: read, no triggers
//	invalid   its frontmatter does not read; the approved one, if any, applies
const (
	skillApproved = "approved"
	skillChanged  = "changed"
	skillNew      = "new"
	skillStarter  = "starter"
	skillLegacy   = "legacy"
	skillInvalid  = "invalid"
)

// brainSkill is a skill file of a brain as the policy sees it.
type brainSkill struct {
	skill.Skill
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Hash identifies the declared policy, for approving it as reviewed.
	Hash string `json:"hash"`
	// Effective is the policy that applies (see the statuses).
	Effective skill.Policy `json:"effective"`
	// Approved is the policy approved, when one is.
	Approved *skill.Policy `json:"approved"`
}

// matches reports whether text names one of the skill's triggers that
// apply.
func (s brainSkill) matches(text string) bool {
	return skill.Skill{Policy: s.Effective}.Matches(text)
}

// maxSkills caps the skill files read from one brain.
const maxSkills = 200

// starterPolicies are the policies the starter skills declare, by file:
// skills the bridge itself wrote into a new brain apply as declared
// until they change.
var starterPolicies = func() map[string]string {
	out := map[string]string{}
	for _, t := range brainTemplates.Skills {
		rel := "skills/" + t.Name
		if s, err := skill.Parse(rel, []byte(t.Content)); err == nil && s.Frontmatter {
			out[rel] = s.Policy.Hash()
		}
	}
	return out
}()

// brainSkills reads brain id's skills and what of them applies.
func (b *App) brainSkills(ctx context.Context, id string) ([]brainSkill, error) {
	br := b.brainMgr.Get(id)
	if br == nil {
		return nil, apperr.New(apperr.NotFound, fmt.Sprintf("brain %q is not running", id))
	}
	files, err := readSkillFiles(br.Dir())
	if err != nil {
		return nil, err
	}
	approved, err := b.store.SkillApprovals(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []brainSkill{}
	for _, f := range files {
		s, perr := skill.Parse(f.rel, f.data)
		if f.err != nil {
			perr = f.err
		}
		bs := brainSkill{Skill: s, Hash: s.Policy.Hash()}
		var ap *skill.Policy
		if a, ok := approved[f.rel]; ok {
			var p skill.Policy
			if json.Unmarshal(a.Policy, &p) == nil {
				p = p.Normalized()
				ap = &p
			}
		}
		bs.Approved = ap
		read := skill.Policy{Risk: skill.RiskRead}.Normalized()
		switch {
		case perr != nil:
			bs.Status, bs.Error, bs.Effective = skillInvalid, perr.Error(), read
			if ap != nil {
				bs.Effective = *ap
			}
		case !s.Frontmatter:
			bs.Status, bs.Effective = skillLegacy, read
			if ap != nil {
				bs.Effective = *ap
			}
		case ap != nil && ap.Hash() == bs.Hash:
			bs.Status, bs.Effective = skillApproved, s.Policy
		case ap != nil:
			bs.Status, bs.Effective = skillChanged, *ap
		case starterPolicies[f.rel] == bs.Hash:
			bs.Status, bs.Effective = skillStarter, s.Policy
		default:
			bs.Status, bs.Effective = skillNew, read
		}
		out = append(out, bs)
	}
	return out, nil
}

type skillFile struct {
	rel  string
	data []byte
	err  error
}

// readSkillFiles reads the skill files in the brain folder dir, through
// the folder's root and following no symlink (see readMemory): the
// bridge reads them outside the sandbox while the brain controls them.
func readSkillFiles(dir string) ([]skillFile, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	skills, err := openSubRoot(root, skillsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("skills/: %w", err)
	}
	defer skills.Close()
	d, err := skills.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := d.ReadDir(maxSkills + 1)
	d.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	var out []skillFile
	for _, e := range entries[:min(len(entries), maxSkills)] {
		name := e.Name()
		switch {
		case e.Type()&fs.ModeSymlink != 0 || strings.HasPrefix(name, "."):
		case e.Type().IsRegular() && skill.IsSkillPath(skillsDir+"/"+name):
			data, err := fsx.ReadBrainFileRoot(skills, name)
			out = append(out, skillFile{rel: skillsDir + "/" + name, data: data, err: err})
		case e.IsDir():
			sub, err := openSubRoot(skills, name)
			if err != nil {
				continue
			}
			if st, err := sub.Lstat("SKILL.md"); err == nil && st.Mode().IsRegular() {
				data, err := fsx.ReadBrainFileRoot(sub, "SKILL.md")
				out = append(out, skillFile{rel: skillsDir + "/" + name + "/SKILL.md", data: data, err: err})
			}
			sub.Close()
		}
	}
	return out, nil
}

// openSubRoot opens the folder name in root, refusing a symlink.
func openSubRoot(root *os.Root, name string) (*os.Root, error) {
	st, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		return nil, fsx.ErrSymlink
	}
	if !st.IsDir() {
		return nil, errors.New("not a folder")
	}
	sub, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	if now, err := sub.Stat("."); err != nil || !os.SameFile(st, now) {
		sub.Close()
		return nil, errors.New("the folder changed while it was opened")
	}
	return sub, nil
}

// skillsView answers GET /api/brains/{id}/skills.
func (b *App) skillsView(ctx context.Context, id string) (any, error) {
	skills, err := b.brainSkills(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"skills": skills, "risks": skill.Risks}, nil
}

// findSkill is brain id's skill at rel.
func (b *App) findSkill(ctx context.Context, id, rel string) (brainSkill, error) {
	skills, err := b.brainSkills(ctx, id)
	if err != nil {
		return brainSkill{}, err
	}
	for _, s := range skills {
		if s.Path == rel {
			return s, nil
		}
	}
	return brainSkill{}, apperr.New(apperr.NotFound, fmt.Sprintf("brain %s has no skill %s", id, rel))
}

// approveSkill answers POST /api/brains/{id}/skills/approve: the owner
// accepts the policy the file declares, provided it is still the one
// reviewed (hash), since the brain can rewrite the file at any moment.
func (b *App) approveSkill(ctx context.Context, id string, req httpapi.SkillApproveRequest) (any, error) {
	s, err := b.findSkill(ctx, id, req.Path)
	if err != nil {
		return nil, err
	}
	if s.Status == skillInvalid {
		return nil, apperr.New(apperr.Invalid, "the frontmatter does not read: "+s.Error)
	}
	if req.Hash != s.Hash {
		return nil, apperr.New(apperr.Conflict, "the skill changed again since you reviewed it — look at it again and approve that")
	}
	if err := b.recordSkillApproval(ctx, id, req.Path, s.Policy, "dashboard"); err != nil {
		return nil, err
	}
	return b.findSkill(ctx, id, req.Path)
}

func (b *App) recordSkillApproval(ctx context.Context, id, rel string, p skill.Policy, by string) error {
	p = p.Normalized()
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := b.store.ApproveSkill(ctx, store.SkillApproval{Brain: id, Path: rel, Hash: p.Hash(), Policy: data, ApprovedAt: time.Now()}); err != nil {
		return err
	}
	b.audit.Append(audit.Entry{Kind: audit.SkillApproved, Brain: id, Skill: rel, Risk: string(p.Risk), By: by,
		Detail: fmt.Sprintf("approve %v, triggers %v, vault %v", p.Approve, p.Triggers, p.Vault), Outcome: "ok"})
	return nil
}

// setSkillPolicy answers PUT /api/brains/{id}/skills/policy: the owner
// edits the skill's metadata.fridolin in the dashboard, which writes it
// into the file and approves it.
func (b *App) setSkillPolicy(ctx context.Context, id string, req httpapi.SkillPolicyRequest) (any, error) {
	if !skill.IsSkillPath(req.Path) {
		return nil, apperr.New(apperr.Invalid, fmt.Sprintf("%q is not a skill file (skills/<name>.md or skills/<name>/SKILL.md)", req.Path))
	}
	risk, err := skill.ParseRisk(req.Risk)
	if err != nil {
		return nil, apperr.New(apperr.Invalid, "risk: "+err.Error())
	}
	br := b.brainMgr.Get(id)
	if br == nil {
		return nil, apperr.New(apperr.NotFound, fmt.Sprintf("brain %q is not running", id))
	}
	p := skill.Policy{Risk: risk, Vault: trimAll(req.Vault), Approve: trimAll(req.Approve), Triggers: trimAll(req.Triggers)}
	err = rewriteSkillFile(br.Dir(), req.Path, func(data []byte) ([]byte, error) {
		out, err := skill.WithPolicy(req.Path, data, p, "")
		if err != nil {
			return nil, apperr.New(apperr.Invalid, err.Error())
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	s, err := b.findSkill(ctx, id, req.Path)
	if err != nil {
		return nil, err
	}
	if err := b.recordSkillApproval(ctx, id, req.Path, s.Policy, "dashboard"); err != nil {
		return nil, err
	}
	return b.findSkill(ctx, id, req.Path)
}

func trimAll(list []string) []string {
	out := []string{}
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// rewriteSkillFile replaces the skill file rel in the brain folder dir
// with what edit makes of it: written next to it and renamed over it,
// through the folder's root, following no symlink.
func rewriteSkillFile(dir, rel string, edit func([]byte) ([]byte, error)) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	cur := root
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		next, err := openSubRoot(cur, p)
		if cur != root {
			cur.Close()
		}
		if err != nil {
			return apperr.New(apperr.NotFound, fmt.Sprintf("%s: %v", path.Dir(rel), err))
		}
		cur = next
	}
	if cur != root {
		defer cur.Close()
	}
	name := parts[len(parts)-1]
	data, err := fsx.ReadBrainFileRoot(cur, name)
	if err != nil {
		return apperr.New(apperr.NotFound, fmt.Sprintf("%s: %v", rel, err))
	}
	out, err := edit(data)
	if err != nil {
		return err
	}
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	tmp := "." + name + ".tmp-" + hex.EncodeToString(suffix[:])
	f, err := fsx.OpenInFolder(cur, tmp, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = cur.Remove(tmp) }()
	if _, err := f.Write(out); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return cur.Rename(tmp, name)
}
