package main

import "github.com/Icewater1337/fridolin/internal/otp"

// Temporary extraction aliases, removed by the following caller rewrite.
type otpRequest = otp.Request
type otpHandoff = otp.Record
type otpHandoffs = otp.Handoff

var (
	otpCode          = otp.Code
	hasMarker        = otp.HasMarker
	otpRequested     = otp.Requested
	newOTPRequestID  = otp.NewRequestID
	sweepOTPHandoffs = otp.Sweep
)
