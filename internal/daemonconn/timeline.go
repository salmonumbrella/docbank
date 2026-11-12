package daemonconn

import (
	"context"

	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/apiclient"
)

func (c *Connection) TimelineDays(ctx context.Context, request api.TimelineDaysRequest) (api.TimelineDaysResponse, error) {
	response, err := c.API().CountTimelineDays(ctx, &apiclient.CountTimelineDaysRequestOptions{Body: &request})
	if err != nil {
		return api.TimelineDaysResponse{}, err
	}
	return *response, nil
}
func (c *Connection) TimelineDay(ctx context.Context, request api.TimelineDayRequest) (api.TimelineDayResponse, error) {
	response, err := c.API().ListTimelineDay(ctx, &apiclient.ListTimelineDayRequestOptions{Body: &request})
	if err != nil {
		return api.TimelineDayResponse{}, err
	}
	return *response, nil
}
