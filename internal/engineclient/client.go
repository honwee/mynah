// Package engineclient wraps the AvatarEngine gRPC contract for cored.
package engineclient

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "mynah/gen/go/proto/avatarengine/v1"
)

type Client struct {
	conn *grpc.ClientConn
	AE   pb.AvatarEngineClient
}

// Dial connects to an AvatarEngine worker (insecure, localhost).
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
	return &Client{conn: conn, AE: pb.NewAvatarEngineClient(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }
