package agents

import (
	"ai/domain"
	"ai/domain/services"
)

// Message — алиас доменного типа (domain.AgentMessage): промпт-сообщение
// агента. MessageType и константы тоже принадлежат домену.
type Message = domain.AgentMessage

// MessageType — тип агентского сообщения.
type MessageType = domain.MessageType

const (
	MessageTypeAI       = domain.MessageTypeAI
	MessageTypeHuman    = domain.MessageTypeHuman
	MessageTypeSystem   = domain.MessageTypeSystem
	MessageTypeGeneric  = domain.MessageTypeGeneric
	MessageTypeFunction = domain.MessageTypeFunction
	MessageTypeTool     = domain.MessageTypeTool
)

// Agent — алиас доменного порта services.Agent (H5a): промпты, доступные
// инструменты и диспетчер вызовов. application-слой зависит от порта, а не от
// пакета agents; конкретные агенты agents/* структурно удовлетворяют ему.
type Agent = services.Agent
