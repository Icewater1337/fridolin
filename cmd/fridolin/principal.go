package main

// PrincipalKind says who called the script API.
type PrincipalKind int

const (
	// CallerAnonymous sent no token.
	CallerAnonymous PrincipalKind = iota
	// CallerScript sent the scripts token.
	CallerScript
	// CallerBrain sent a brain's token.
	CallerBrain
)

// Principal is who called the script API.
type Principal struct {
	Kind  PrincipalKind
	Brain string // for CallerBrain
}

// Via is how a message the caller sent is marked in history.
func (p Principal) Via() string {
	switch p.Kind {
	case CallerScript:
		return "script"
	case CallerBrain:
		return "brain"
	}
	return "api"
}
