package app

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/connections"
	"github.com/Icewater1337/fridolin/internal/doctor"
	"github.com/Icewater1337/fridolin/internal/gitident"
)

// connectionDoctorTimeout bounds doctor's key check.
const connectionDoctorTimeout = 10 * time.Second

// connectionDoctorChecks check that the OpenAI and ElevenLabs keys and
// the GitLab token work, where one is set (docs/CONNECTIONS.md).
var connectionDoctorChecks = []doctor.Check{
	connectionDoctorCheck(connections.OpenAI),
	connectionDoctorCheck(connections.ElevenLabs),
	connectionDoctorCheck(gitident.GitLab),
}

func connectionDoctorCheck(id string) doctor.Check {
	keyFile := func(env *doctor.Env) string {
		if id == gitident.GitLab {
			return filepath.Join(config.Layout{Root: env.Config.DataDir}.LocalSecrets(), gitident.GitLabFile)
		}
		return filepath.Join(config.Layout{Root: env.Config.DataDir}.Connections(), id+".json")
	}
	title := connections.Title(id)
	if id == gitident.GitLab {
		title = "GitLab"
	}
	return doctor.Check{
		ID: id, Title: title + " connection",
		Applies: func(env *doctor.Env) bool {
			if env.Config == nil {
				return false
			}
			_, err := env.Stat(keyFile(env))
			return err == nil
		},
		Run: func(ctx context.Context, env *doctor.Env) doctor.Result {
			fix := "set the key again on the dashboard's Integrations page, or remove it there"
			if id == gitident.GitLab {
				fix = "make a new access token in GitLab (read_user, and read_repository and write_repository for FrIDE) and save it on the dashboard's Integrations page, or remove the connection there"
			}
			if env.CheckConnection == nil {
				return doctor.Result{Status: doctor.Warn, Detail: "doctor cannot ask " + title + " here", Fix: fix}
			}
			detail, err := env.CheckConnection(ctx, env.Config.DataDir, id)
			if err != nil {
				return doctor.Result{Status: doctor.Warn, Detail: err.Error(), Fix: fix}
			}
			return doctor.Result{Status: doctor.OK, Detail: detail}
		},
	}
}

// checkConnectionKey is doctor.Env.CheckConnection on this system.
func checkConnectionKey(ctx context.Context, dataDir, id string) (string, error) {
	if id == gitident.GitLab {
		c, ok, err := gitident.Store{Dir: config.Layout{Root: dataDir}.LocalSecrets()}.GitLab()
		if err != nil {
			return "", err
		}
		if !ok {
			return "", errors.New("no GitLab connection: set one on the dashboard's Integrations page")
		}
		ctx, cancel := context.WithTimeout(ctx, connectionDoctorTimeout)
		defer cancel()
		user, err := gitident.Check(ctx, nil, c)
		if err != nil {
			return "", err
		}
		return "GitLab at " + c.Host() + " knows the token as " + user, nil
	}
	s := connections.New(connections.Options{Dir: config.Layout{Root: dataDir}.Connections()})
	return s.Check(ctx, id, connectionDoctorTimeout)
}
