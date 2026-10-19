package main

import "github.com/Icewater1337/fridolin/internal/google"

type GoogleAuth = google.Auth
type GoogleOptions = google.Options
type GoogleEndpoints = google.Endpoints
type GoogleClient = google.Client
type GoogleToken = google.Token
type GoogleCalendars = google.Calendars
type CalendarEntry = google.CalendarEntry
type CalendarEvent = google.CalendarEvent
type CalendarListing = google.CalendarListing
type NewEvent = google.NewEvent
type Reminder = google.Reminder
type BusySlot = google.BusySlot

var NewGoogleAuth = google.New
var ParseGoogleClient = google.ParseClient
var GoogleCalendarsOf = google.CalendarsOf
var GoogleCallProblem = google.CallProblem
var GoogleCalendarEnv = google.CalendarEnv
var GoogleTokenFile = google.TokenFile
var GoogleCredentialsFile = google.CredentialsFile
var GoogleMaskEmail = google.MaskEmail
var ErrGoogleNotRevoked = google.ErrNotRevoked
var ErrGoogleNoClient = google.ErrNoClient
var GoogleDecodeError = google.DecodeError
