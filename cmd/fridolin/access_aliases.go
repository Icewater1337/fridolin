package main

import "github.com/Icewater1337/fridolin/internal/access"

type accessPolicy = access.Policy

var accessNumberAllowed = access.NumberAllowed
var accessGroupAllowed = access.GroupAllowed
var accessGroupSenderAllowed = access.GroupSenderAllowed
var accessAuthorize = access.Authorize
var accessDecide = access.Decide

var accessAddress = access.AddressGroup
var accessStripMentions = access.StripMentions
