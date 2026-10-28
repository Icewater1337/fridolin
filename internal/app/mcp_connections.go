package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/Icewater1337/fridolin/internal/connections"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/mcp/tools"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// The brains' tools of Fridolin's connections (docs/CONNECTIONS.md): a
// run has them only while the connection has a key and its brain is in
// connections.<id>.brains. The bridge makes the call with the key, which
// the run never sees, and counts it against the brain's monthly limit.
// Files go by path: a recording the run names is read through os.Root
// from its folder, its media inbox or its temp folder (openRunFile), and
// speech is written into its temp folder.

// mayUseConnection reports whether brain's runs have conn's tools.
func (b *App) mayUseConnection(brain, conn string) bool {
	return b.conns != nil && slices.Contains(connectionSettingsOf(b.cfgs.Get()).Of(conn).Brains, brain) && b.conns.Configured(conn)
}

// connectionCaller is the run as a caller of conn, with its brain's limit.
func (b *App) connectionCaller(c mcpCaller, conn string) connections.Caller {
	return connections.BrainCaller(c.run.Brain, c.run.ID, connectionSettingsOf(b.cfgs.Get()).Of(conn).BrainMonthlyUSD)
}

func (b *App) registerConnectionTools(r *tools.Set[mcpCaller]) {
	may := func(conn string) func(mcpCaller) bool {
		return func(c mcpCaller) bool { return b.mayUseConnection(c.run.Brain, conn) }
	}
	pathArg := tools.Str("The recording: an absolute path in your folder, your media inbox or your temp folder, such as a voice note's path from the message. At most 25 MiB.")
	languageArg := tools.Str("Its language as a code such as de or en, when you know it; it helps.")
	r.Register(mcp.Entry[mcpCaller]{
		Name: "openai_transcribe", Available: may(connections.OpenAI),
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{Description: "Turn a recording into text with OpenAI, through Fridolin's key. The recording leaves this machine for OpenAI. It answers {\"text\"}.",
				InputSchema: tools.Schema(map[string]any{
					"path":     pathArg,
					"language": languageArg,
					"prompt":   tools.Str("Names and words to expect, so that they are spelled right."),
					"model":    tools.Str("The model; " + connections.DefaultOpenAITranscribe + " when left out."),
				}, "path")}
		},
		Call: r.Guarded(skill.RiskWrite, b.callConnectionTranscribe(connections.OpenAI)),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name: "elevenlabs_transcribe", Available: may(connections.ElevenLabs),
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{Description: "Turn a recording into text with ElevenLabs, through Fridolin's key. The recording leaves this machine for ElevenLabs. It answers {\"text\", \"language\"}.",
				InputSchema: tools.Schema(map[string]any{
					"path":     pathArg,
					"language": languageArg,
					"model":    tools.Str("The model; " + connections.DefaultElevenLabsTranscribe + " when left out."),
				}, "path")}
		},
		Call: r.Guarded(skill.RiskWrite, b.callConnectionTranscribe(connections.ElevenLabs)),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name: "elevenlabs_speak", Available: may(connections.ElevenLabs),
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{Description: "Read a text aloud with ElevenLabs, through Fridolin's key, into an audio file in your temp folder. It answers {\"path\", \"content_type\", \"characters\"}. The file stays on this machine: the bridge cannot send audio to WhatsApp yet.",
				InputSchema: tools.Schema(map[string]any{
					"text":   tools.Str("What to read, at most 10,000 characters."),
					"voice":  tools.Str("A voice id from elevenlabs_voices; the default voice when left out."),
					"model":  tools.Str("The model; " + connections.DefaultElevenLabsSpeech + " when left out."),
					"format": tools.Str("The output format, such as mp3_44100_128 (the default) or pcm_16000."),
				}, "text")}
		},
		Call: r.Guarded(skill.RiskWrite, b.callElevenLabsSpeak),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name: "elevenlabs_voices", Available: may(connections.ElevenLabs),
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{Description: "List the ElevenLabs account's voices, for elevenlabs_speak. It answers {\"voices\": [{\"id\", \"name\"}]}.",
				InputSchema: tools.Schema(nil)}
		},
		Call: b.callElevenLabsVoices,
	})
}

// connectionResult is what the model reads of a call that failed.
func connectionResult(err error) *mcp.CallResult {
	return mcp.ErrorResult(err.Error())
}

// readRecording reads the recording a run names, at most
// connections.MaxAudio.
func (b *App) readRecording(brain, path string) (connections.Audio, error) {
	f, err := b.openRunFile(brain, "path", path)
	if err != nil {
		return connections.Audio{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, connections.MaxAudio+1))
	if err != nil {
		return connections.Audio{}, err
	}
	if len(data) > connections.MaxAudio {
		return connections.Audio{}, fmt.Errorf("the recording is more than %d MiB", connections.MaxAudio>>20)
	}
	return connections.Audio{Data: data, Filename: filepath.Base(path), ContentType: connections.AudioType(path)}, nil
}

func (b *App) callConnectionTranscribe(conn string) tools.Call[mcpCaller] {
	return func(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
		var args struct {
			Path     string `json:"path"`
			Language string `json:"language"`
			Prompt   string `json:"prompt"`
			Model    string `json:"model"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ErrorResult(`the arguments are {"path": "<absolute path>"}: ` + err.Error()), nil
		}
		audio, err := b.readRecording(c.run.Brain, args.Path)
		if err != nil {
			return mcp.ErrorResult(err.Error()), nil
		}
		t, err := b.conns.Transcribe(ctx, conn, b.connectionCaller(c, conn), connections.TranscribeRequest{Audio: audio, Model: args.Model, Language: args.Language, Prompt: args.Prompt})
		if err != nil {
			return connectionResult(err), nil
		}
		if conn == connections.OpenAI {
			return jsonResult(map[string]string{"text": t.Text}), nil
		}
		return jsonResult(map[string]string{"text": t.Text, "language": t.Language}), nil
	}
}

func (b *App) callElevenLabsSpeak(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args struct {
		Text   string `json:"text"`
		Voice  string `json:"voice"`
		Model  string `json:"model"`
		Format string `json:"format"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult(`the arguments are {"text": "…"}: ` + err.Error()), nil
	}
	sp, err := b.conns.Speak(ctx, b.connectionCaller(c, connections.ElevenLabs), connections.SpeakRequest{Text: args.Text, Voice: args.Voice, Model: args.Model, Format: args.Format})
	if err != nil {
		return connectionResult(err), nil
	}
	tmp := brainStateOf(AbsPath(b.cfgs.Boot().DataDir), c.run.Brain).tmp
	path, err := writeNewFile(tmp, "speech", time.Now().UTC().Format("20060102-150405"), connections.SpeechExt(sp.Format, sp.ContentType), sp.Audio)
	if err != nil {
		return mcp.ErrorResult("could not write the audio: " + err.Error()), nil
	}
	return jsonResult(map[string]any{"path": path, "content_type": sp.ContentType, "characters": sp.Characters}), nil
}

// writeNewFile writes data to a new file stem+ext in dir below base, 0600,
// or stem-2+ext and on when that is taken, and returns its path. The run
// writes in base too: every step goes through os.Root, so a symlink it
// planted there cannot lead the write out of base.
func writeNewFile(base, dir, stem, ext string, data []byte) (string, error) {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := root.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for i := 1; ; i++ {
		name := stem + ext
		if i > 1 {
			name = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		rel := filepath.Join(dir, name)
		f, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) && i < 100 {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			root.Remove(rel)
			return "", err
		}
		return filepath.Join(base, rel), nil
	}
}

func (b *App) callElevenLabsVoices(ctx context.Context, c mcpCaller, _ json.RawMessage) (*mcp.CallResult, error) {
	vs, err := b.conns.Voices(ctx, b.connectionCaller(c, connections.ElevenLabs))
	if err != nil {
		return connectionResult(err), nil
	}
	return jsonResult(map[string]any{"voices": vs}), nil
}
