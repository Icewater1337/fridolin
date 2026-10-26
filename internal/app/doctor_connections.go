package app

import (
	"context"
	"path/filepath"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/connections"
	"github.com/Icewater1337/fridolin/internal/doctor"
)

// connectionDoctorTimeout bounds doctor's key check.
const connectionDoctorTimeout = 10 * time.Second

// connectionDoctorChecks check that the OpenAI and ElevenLabs keys work,
// where one is set (docs/CONNECTIONS.md).
var connectionDoctorChecks = []doctor.Check{
	connectionDoctorCheck(connections.OpenAI),
	connectionDoctorCheck(connections.ElevenLabs),
}

func connectionDoctorCheck(id string) doctor.Check {
	keyFile := func(env *doctor.Env) string {
		return filepath.Join(config.Layout{Root: env.Config.DataDir}.Connections(), id+".json")
	}
	return doctor.Check{
		ID: id, Title: connections.Title(id) + " connection",
		Applies: func(env *doctor.Env) bool {
			if env.Config == nil {
				return false
			}
			_, err := env.Stat(keyFile(env))
			return err == nil
		},
		Run: func(ctx context.Context, env *doctor.Env) doctor.Result {
			fix := "set the key again on the dashboard's Integrations page, or remove it there"
			if env.CheckConnection == nil {
				return doctor.Result{Status: doctor.Warn, Detail: "doctor cannot ask " + connections.Title(id) + " here", Fix: fix}
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
	s := connections.New(connections.Options{Dir: config.Layout{Root: dataDir}.Connections()})
	return s.Check(ctx, id, connectionDoctorTimeout)
}
