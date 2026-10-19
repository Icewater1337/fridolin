// Package outbound owns reply chunking, poll parsing, chat-facing messages,
// send contracts and paced partial-send reporting. It does not connect a
// transport or own durable delivery, retry, history or shutdown lifecycles.
package outbound
