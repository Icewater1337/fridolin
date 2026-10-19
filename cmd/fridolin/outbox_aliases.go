package main

import outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"

type Outbox = outboundmsg.Outbox

var newOutbox = outboundmsg.NewOutbox

const outboxTTL = outboundmsg.OutboxTTL
const outboxPace = outboundmsg.OutboxPace
