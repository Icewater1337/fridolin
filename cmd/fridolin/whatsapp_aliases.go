package main

import "github.com/Icewater1337/fridolin/internal/whatsapp"

type WhatsAppClient = whatsapp.Client
type waOptions = whatsapp.Options
type waSettings = whatsapp.Settings
type waSentPoll = whatsapp.SentPoll
type waVote = whatsapp.Vote
type waGroupRules = whatsapp.GroupRules
type waOutbound = whatsapp.Outbound
type waText = whatsapp.Text
type waIdentity = whatsapp.Identity

var newWhatsAppClient = whatsapp.New
var runConnectLoop = whatsapp.RunConnectLoop

type IgnoredHandler = whatsapp.IgnoredHandler
