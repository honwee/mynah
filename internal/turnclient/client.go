// Package turnclient wraps the TurnDetector gRPC contract as a
// core.TurnDetector: cored hands it the just-finished utterance's audio and
// gets back an end-of-turn probability + thresholded decision. Unary,
// localhost, off the hot media path.
package turnclient

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "mynah/gen/go/proto/turn/v1"
)

type Client struct {
	conn *grpc.ClientConn
	td   pb.TurnDetectorClient
}

// Dial connects to a TurnDetector worker (insecure, localhost).
func Dial(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, td: pb.NewTurnDetectorClient(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Health reports whether the model is loaded.
func (c *Client) Health(ctx context.Context) (bool, string, error) {
	rep, err := c.td.Health(ctx, &pb.HealthRequest{})
	if err != nil {
		return false, "", err
	}
	return rep.Ready, rep.Detail, nil
}

// Predict implements core.TurnDetector.
func (c *Client) Predict(ctx context.Context, audioPCM []byte, lang string) (float32, bool, error) {
	rep, err := c.td.Predict(ctx, &pb.PredictRequest{AudioPcm: audioPCM, Language: lang})
	if err != nil {
		return 0, false, err
	}
	return rep.EotProb, rep.EndOfTurn, nil
}
