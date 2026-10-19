// Package outbound owns reply chunking, polls, chat-facing messages and
// durable delivery over a transport. The outbox persists each reply, resumes
// at the first unsent part and joins its worker on Stop. Command composition
// supplies the live transport and confirmed-delivery history callback.
package outbound
