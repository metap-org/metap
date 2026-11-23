// Package events is the one place that knows what a platform domain event looks like: its topic
// (routing key) naming, its typed payload, and how a service emits one through the outbox in the
// same transaction as its business write. Producers (pkg/workflow, pkg/cron, a service's own
// code) and consumers (notification worker, cron trigger listener, control-plane) all go through
// it, so a topic is never built by string concatenation or parsed by suffix in more than this file.
//
// Topics are dot-separated words; an entity name may itself contain dots ("waf.zones"), so the
// kind is the trailing part and the entity is everything before it:
//
//	<entity>.workflow.transitioned
//	<entity>.record.created | updated | deleted
//	<namespace>.<name>            custom events (see Emit)
package events

import (
	"fmt"
	"strings"
)

// RecordAction is the verb in an <entity>.record.<action> topic.
type RecordAction string

const (
	RecordCreated RecordAction = "created"
	RecordUpdated RecordAction = "updated"
	RecordDeleted RecordAction = "deleted"
)

const (
	transitionedSuffix = ".workflow.transitioned"
	recordInfix        = ".record."

	// AnyTransition binds every entity's workflow-transition topic ("#" matches any number of
	// dot-separated words, so a domain-namespaced entity name still matches).
	AnyTransition = "#" + transitionedSuffix
	// AnyRecordEvent binds every entity's record-lifecycle topics.
	AnyRecordEvent = "#" + recordInfix + "*"
	// All is the catch-all binding.
	All = "#"
)

// WorkflowTransitioned is the topic for a workflow transition of entity.
func WorkflowTransitioned(entity string) string { return entity + transitionedSuffix }

// Record is the topic for a record lifecycle event of entity.
func Record(entity string, action RecordAction) string {
	return entity + recordInfix + string(action)
}

// RecordPattern binds one record action of every entity whose name matches entityPattern, e.g.
// RecordPattern("waf.*", "") for every action on every waf entity ("*" action = any).
func RecordPattern(entityPattern string, action RecordAction) string {
	a := string(action)
	if a == "" {
		a = "*"
	}
	return entityPattern + recordInfix + a
}

// Kind classifies a topic.
type Kind int

const (
	KindOther Kind = iota
	KindTransitioned
	KindRecord
)

// Topic is a parsed routing key.
type Topic struct {
	Kind   Kind
	Entity string
	Action RecordAction // only for KindRecord
}

// ParseTopic classifies a routing key. Anything that is not a platform lifecycle topic is
// KindOther — not an error: under a catch-all subscription it is the expected case.
func ParseTopic(routingKey string) Topic {
	if entity, ok := strings.CutSuffix(routingKey, transitionedSuffix); ok && entity != "" {
		return Topic{Kind: KindTransitioned, Entity: entity}
	}
	if i := strings.LastIndex(routingKey, recordInfix); i > 0 {
		switch action := RecordAction(routingKey[i+len(recordInfix):]); action {
		case RecordCreated, RecordUpdated, RecordDeleted:
			return Topic{Kind: KindRecord, Entity: routingKey[:i], Action: action}
		}
	}
	return Topic{Kind: KindOther}
}

// ValidateTopic checks a topic a service wants to publish: non-empty dot-separated words of
// letters, digits, '_' and '-', no AMQP wildcards, and not one of the platform lifecycle shapes
// (those are emitted only by the platform, from the write path that owns them).
func ValidateTopic(topic string) error {
	if topic == "" {
		return fmt.Errorf("event topic is empty")
	}
	for _, word := range strings.Split(topic, ".") {
		if word == "" {
			return fmt.Errorf("event topic %q has an empty word", topic)
		}
		for _, c := range word {
			ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
			if !ok {
				return fmt.Errorf("event topic %q: invalid character %q (wildcards are for subscribers only)", topic, c)
			}
		}
	}
	if ParseTopic(topic).Kind != KindOther {
		return fmt.Errorf("event topic %q is reserved for platform record/workflow events", topic)
	}
	return nil
}
