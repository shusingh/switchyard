// Package openai defines the subset of the OpenAI HTTP API that Switchyard
// reads and writes: the request fields needed for routing, error bodies, and
// server-sent event framing.
//
// It is a leaf package shared by the router, the simulated engine, and the
// load generator, so all three agree on the wire format. It deliberately does
// not model full request or response schemas: the router forwards bodies
// unchanged and only decodes what it needs.
package openai
