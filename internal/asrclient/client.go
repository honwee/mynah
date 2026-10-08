// Package asrclient wraps the AsrEngine gRPC contract for cored: it forwards
// browser-mic Opus packets to the ASR worker and surfaces VAD edges (used for
// barge-in) plus final per-utterance transcripts.
package asrclient

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "mynah/gen/go/proto/asr/v1"
)

type Client struct {
	conn *grpc.ClientConn
	ASR  pb.AsrEngineClient
}

// Dial connects to an AsrEngine worker (insecure, localhost).
func Dial(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(16*1024*1024),
			grpc.MaxCallSendMsgSize(16*1024*1024),
		),
	)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, ASR: pb.NewAsrEngineClient(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }
