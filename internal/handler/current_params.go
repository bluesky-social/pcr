package handler

import (
	"net/url"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

func parseCurrentParams(q url.Values) (model.CurrentParams, *paramError) {
	params := model.CurrentParams{
		ForTeam:    q.Get("for_team"),
		Scopes:     q["scope"],
		Severities: q["severity"],
		EventType:  q.Get("type"),
	}
	if err := parseIntParam(q, "limit", &params.Limit); err != nil {
		return params, err
	}
	if err := parseIntParam(q, "offset", &params.Offset); err != nil {
		return params, err
	}
	return params, nil
}
