package shredstream_client

import (
	"context"
	"crypto/tls"
	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/pvaronik/jito-go/pkg"
	"github.com/pvaronik/jito-go/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type client struct {
	GrpcConn *grpc.ClientConn
	RpcConn  *rpc.Client

	ShredstreamClient proto.ShredstreamClient

	Auth *pkg.AuthenticationService
}

// disabled until fully working :)
func new(grpcDialURL string, rpcClient *rpc.Client, privateKey solana.PrivateKey, tlsConfig *tls.Config, opts ...grpc.DialOption) (*client, error) {
	if tlsConfig != nil {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))
	}

	conn, err := pkg.CreateAndObserveGRPCConn(context.Background(), grpcDialURL, opts...)
	if err != nil {
		return nil, err
	}

	shredstreamService := proto.NewShredstreamClient(conn)
	authService := pkg.NewAuthenticationService(conn, privateKey)
	if err = authService.AuthenticateAndRefresh(proto.Role_SHREDSTREAM_SUBSCRIBER); err != nil {
		return nil, err
	}

	return &client{
		GrpcConn:          conn,
		RpcConn:           rpcClient,
		ShredstreamClient: shredstreamService,
		Auth:              authService,
	}, nil
}

func (c *client) SendHeartbeat(count uint64, opts ...grpc.CallOption) (*proto.HeartbeatResponse, error) {
	return c.ShredstreamClient.SendHeartbeat(c.Auth.GrpcCtx, &proto.Heartbeat{Count: count}, opts...)
}
