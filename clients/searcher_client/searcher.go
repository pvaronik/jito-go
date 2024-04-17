package searcher_client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/blocto/solana-go-sdk/types"
	"github.com/mr-tron/base58"
	"math/big"
	"math/rand"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/pvaronik/jito-go/pkg"
	"github.com/pvaronik/jito-go/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type Client struct {
	GrpcConn    *grpc.ClientConn
	RpcConn     *rpc.Client
	JitoRpcConn *rpc.Client

	SearcherService       proto.SearcherServiceClient
	SubscribeBundleStream proto.SearcherService_SubscribeBundleResultsClient

	Auth *pkg.AuthenticationService

	ErrChan chan error
}

// New creates a new Searcher Client instance.
func New(grpcDialURL string, jitoRpcClient, rpcClient *rpc.Client, privateKey solana.PrivateKey, tlsConfig *tls.Config, opts ...grpc.DialOption) (*Client, error) {
	if tlsConfig != nil {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))
	}

	conn, err := pkg.CreateAndObserveGRPCConn(context.Background(), grpcDialURL, opts...)
	if err != nil {
		return nil, err
	}

	searcherService := proto.NewSearcherServiceClient(conn)
	authService := pkg.NewAuthenticationService(conn, privateKey)
	if err = authService.AuthenticateAndRefresh(proto.Role_SEARCHER); err != nil {
		return nil, err
	}

	subBundleRes, err := searcherService.SubscribeBundleResults(authService.GrpcCtx, &proto.SubscribeBundleResultsRequest{})
	if err != nil {
		return nil, err
	}

	return &Client{
		GrpcConn:              conn,
		RpcConn:               rpcClient,
		JitoRpcConn:           jitoRpcClient,
		SearcherService:       searcherService,
		SubscribeBundleStream: subBundleRes,
		Auth:                  authService,
		ErrChan:               make(chan error),
	}, nil
}

// NewMempoolStreamAccount creates a new mempool subscription on specific Solana accounts.
func (c *Client) NewMempoolStreamAccount(accounts, regions []string) (proto.SearcherService_SubscribeMempoolClient, error) {
	return c.SearcherService.SubscribeMempool(c.Auth.GrpcCtx, &proto.MempoolSubscription{
		Msg: &proto.MempoolSubscription_WlaV0Sub{
			WlaV0Sub: &proto.WriteLockedAccountSubscriptionV0{
				Accounts: accounts,
			},
		},
		Regions: regions,
	})
}

// NewMempoolStreamProgram creates a new mempool subscription on specific Solana programs.
func (c *Client) NewMempoolStreamProgram(programs, regions []string) (proto.SearcherService_SubscribeMempoolClient, error) {
	return c.SearcherService.SubscribeMempool(c.Auth.GrpcCtx, &proto.MempoolSubscription{
		Msg: &proto.MempoolSubscription_ProgramV0Sub{
			ProgramV0Sub: &proto.ProgramSubscriptionV0{
				Programs: programs,
			},
		},
		Regions: regions,
	})
}

type SubscribeAccountsMempoolTransactionsPayload struct {
	Ctx      context.Context
	Accounts []string
	Regions  []string
	TxCh     chan *solana.Transaction
	ErrCh    chan error
}

type SubscribeProgramsMempoolTransactionsPayload struct {
	Ctx      context.Context
	Accounts []string
	Regions  []string
	TxCh     chan *solana.Transaction
	ErrCh    chan error
}

// SubscribeAccountsMempoolTransactions subscribes to the mempool transactions of the provided accounts.
func (c *Client) SubscribeAccountsMempoolTransactions(payload *SubscribeAccountsMempoolTransactionsPayload) error {
	sub, err := c.NewMempoolStreamAccount(payload.Accounts, payload.Regions)
	if err != nil {
		return err
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				if err = c.SubscribeAccountsMempoolTransactions(payload); err != nil {
					payload.ErrCh <- fmt.Errorf("SubscribeAccountsMempoolTransactions: recovered from panic but unable to restart sub stream: %w", err)
					return
				}
			}
		}()
		for {
			select {
			case <-payload.Ctx.Done():
				return
			default:
				var receipt *proto.PendingTxNotification
				receipt, err = sub.Recv()
				if err != nil {
					c.ErrChan <- fmt.Errorf("SubscribeAccountsMempoolTransactions: failed to receive mempool notification: %w", err)
					continue
				}

				for _, transaction := range receipt.Transactions {
					go func(transaction *proto.Packet) {
						var tx *solana.Transaction
						tx, err = pkg.ConvertProtobufPacketToTransaction(transaction)
						if err != nil {
							c.ErrChan <- fmt.Errorf("SubscribeAccountsMempoolTransactions: failed to convert protobuf packet to transaction: %w", err)
							return
						}

						payload.TxCh <- tx
					}(transaction)
				}
			}
		}
	}()

	return nil
}

// SubscribeProgramsMempoolTransactions subscribes to the mempool transactions of the provided programs.
func (c *Client) SubscribeProgramsMempoolTransactions(payload *SubscribeProgramsMempoolTransactionsPayload) error {
	sub, err := c.NewMempoolStreamProgram(payload.Accounts, payload.Regions)
	if err != nil {
		return err
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				if err = c.SubscribeProgramsMempoolTransactions(payload); err != nil {
					payload.ErrCh <- fmt.Errorf("SubscribeProgramsMempoolTransactions: recovered from panic but unable to restart sub stream: %w", err)
					return
				}
			}
		}()
		for {
			select {
			case <-payload.Ctx.Done():
				return
			default:
				var receipt *proto.PendingTxNotification
				receipt, err = sub.Recv()
				if err != nil {
					c.ErrChan <- fmt.Errorf("SubscribeProgramsMempoolTransactions: failed to receive mempool notification: %w", err)
					continue
				}

				for _, transaction := range receipt.Transactions {
					go func(transaction *proto.Packet) {
						var tx *solana.Transaction
						tx, err = pkg.ConvertProtobufPacketToTransaction(transaction)
						if err != nil {
							c.ErrChan <- fmt.Errorf("SubscribeProgramsMempoolTransactions: failed to convert protobuf packet to transaction: %w", err)
							return
						}

						payload.TxCh <- tx
					}(transaction)
				}
			}
		}
	}()

	return nil
}

func (c *Client) GetRegions(opts ...grpc.CallOption) (*proto.GetRegionsResponse, error) {
	return c.SearcherService.GetRegions(c.Auth.GrpcCtx, &proto.GetRegionsRequest{}, opts...)
}

func (c *Client) GetConnectedLeaders(opts ...grpc.CallOption) (*proto.ConnectedLeadersResponse, error) {
	return c.SearcherService.GetConnectedLeaders(c.Auth.GrpcCtx, &proto.ConnectedLeadersRequest{}, opts...)
}

func (c *Client) GetConnectedLeadersRegioned(regions []string, opts ...grpc.CallOption) (*proto.ConnectedLeadersRegionedResponse, error) {
	return c.SearcherService.GetConnectedLeadersRegioned(c.Auth.GrpcCtx, &proto.ConnectedLeadersRegionedRequest{Regions: regions}, opts...)
}

func (c *Client) GetTipAccounts(opts ...grpc.CallOption) (*proto.GetTipAccountsResponse, error) {
	return c.SearcherService.GetTipAccounts(c.Auth.GrpcCtx, &proto.GetTipAccountsRequest{}, opts...)
}

// GetRandomTipAccount returns a random Jito TipAccount.
func (c *Client) GetRandomTipAccount(opts ...grpc.CallOption) (string, error) {
	resp, err := c.GetTipAccounts(opts...)
	if err != nil {
		return "", err
	}

	return resp.Accounts[rand.Intn(len(resp.Accounts))], nil
}

func (c *Client) GetNextScheduledLeader(regions []string, opts ...grpc.CallOption) (*proto.NextScheduledLeaderResponse, error) {
	return c.SearcherService.GetNextScheduledLeader(c.Auth.GrpcCtx, &proto.NextScheduledLeaderRequest{Regions: regions}, opts...)
}

// NewBundleSubscriptionResults creates a new bundle subscription, allowing to receive information about broadcasted bundles.
func (c *Client) NewBundleSubscriptionResults(opts ...grpc.CallOption) (proto.SearcherService_SubscribeBundleResultsClient, error) {
	return c.SearcherService.SubscribeBundleResults(c.Auth.GrpcCtx, &proto.SubscribeBundleResultsRequest{}, opts...)
}

// BroadcastBundle sends a bundle of transactions on chain thru Jito.
func (c *Client) BroadcastBundle(transactions []types.Transaction, opts ...grpc.CallOption) (*proto.SendBundleResponse, error) {
	packets, err := assemblePackets(transactions)
	if err != nil {
		return nil, err
	}

	return c.SearcherService.SendBundle(c.Auth.GrpcCtx, &proto.SendBundleRequest{Bundle: &proto.Bundle{Packets: packets, Header: nil}}, opts...)
}

// BroadcastBundleWithConfirmation sends a bundle of transactions on chain thru Jito BlockEngine and waits for its confirmation.
func (c *Client) BroadcastBundleWithConfirmation(ctx context.Context, transactions []types.Transaction, opts ...grpc.CallOption) (*proto.SendBundleResponse, error) {
	bloctoBundleSignatures := pkg.BatchExtractSigFromTx(transactions)

	bundleSignatures := make([]solana.Signature, 0, len(bloctoBundleSignatures))
	for _, sig := range bloctoBundleSignatures {
		bundleSignatures = append(bundleSignatures, solana.MustSignatureFromBase58(base58.Encode(sig)))
	}
	resp, err := c.BroadcastBundle(transactions, opts...)
	if err != nil {
		return nil, err
	}

	retries := 5
	for i := 0; i < retries; i++ {
		select {
		case <-c.Auth.GrpcCtx.Done():
			return nil, c.Auth.GrpcCtx.Err()
		default:

			// waiting 5s to check bundle result
			time.Sleep(5 * time.Second)

			var bundleResult *proto.BundleResult
			bundleResult, err = c.SubscribeBundleStream.Recv()
			if err != nil {
				continue
			}

			if err = c.handleBundleResult(bundleResult); err != nil {
				return nil, err
			}

			var start = time.Now()
			var statuses *rpc.GetSignatureStatusesResult

			for {
				statuses, err = c.RpcConn.GetSignatureStatuses(ctx, false, bundleSignatures...)
				if err != nil {
					return nil, err
				}
				ready := true

				for _, status := range statuses.Value {
					if status == nil {
						ready = false
						break
					}
				}

				if ready {
					break
				}

				if time.Since(start) > 15*time.Second {
					return nil, errors.New("operation timed out after 15 seconds")
				} else {
					time.Sleep(1 * time.Second)
				}
			}

			for _, status := range statuses.Value {
				if status.ConfirmationStatus != rpc.ConfirmationStatusProcessed && status.ConfirmationStatus != rpc.ConfirmationStatusConfirmed {
					return nil, errors.New("searcher service did not provide bundle status in time")
				}
			}

			return resp, nil
		}
	}

	return nil, fmt.Errorf("error waiting for max retries exceeded")
}

func (c *Client) handleBundleResult(bundleResult *proto.BundleResult) error {
	switch bundleResult.Result.(type) {
	case *proto.BundleResult_Accepted:
		break
	case *proto.BundleResult_Rejected:
		rejected := bundleResult.Result.(*proto.BundleResult_Rejected)
		switch rejected.Rejected.Reason.(type) {
		case *proto.Rejected_SimulationFailure:
			rejection := rejected.Rejected.GetSimulationFailure()
			return NewSimulationFailureError(rejection.TxSignature, rejection.GetMsg())
		case *proto.Rejected_StateAuctionBidRejected:
			rejection := rejected.Rejected.GetStateAuctionBidRejected()
			return NewStateAuctionBidRejectedError(rejection.AuctionId, rejection.SimulatedBidLamports)
		case *proto.Rejected_WinningBatchBidRejected:
			rejection := rejected.Rejected.GetWinningBatchBidRejected()
			return NewWinningBatchBidRejectedError(rejection.AuctionId, rejection.SimulatedBidLamports)
		case *proto.Rejected_InternalError:
			rejection := rejected.Rejected.GetInternalError()
			return NewInternalError(rejection.Msg)
		case *proto.Rejected_DroppedBundle:
			rejection := rejected.Rejected.GetDroppedBundle()
			return NewDroppedBundle(rejection.Msg)
		default:
			return nil
		}
	}
	return nil
}

type SimulateBundleConfig struct {
	PreExecutionAccountsConfigs  []ExecutionAccounts `json:"preExecutionAccountsConfigs"`
	PostExecutionAccountsConfigs []ExecutionAccounts `json:"postExecutionAccountsConfigs"`
}

type ExecutionAccounts struct {
	Encoding  string   `json:"encoding"`
	Addresses []string `json:"addresses"`
}

type SimulateBundleParams struct {
	EncodedTransactions []string `json:"encodedTransactions"`
}

type SimulatedBundleResponse struct {
	Context interface{}                   `json:"context"`
	Value   SimulatedBundleResponseStruct `json:"value"`
}

type SimulatedBundleResponseStruct struct {
	Summary           interface{}         `json:"summary"`
	TransactionResult []TransactionResult `json:"transactionResults"`
}

type TransactionResult struct {
	Err                   interface{} `json:"err,omitempty"`
	Logs                  []string    `json:"logs,omitempty"`
	PreExecutionAccounts  []Account   `json:"preExecutionAccounts,omitempty"`
	PostExecutionAccounts []Account   `json:"postExecutionAccounts,omitempty"`
	UnitsConsumed         *int        `json:"unitsConsumed,omitempty"`
	ReturnData            *ReturnData `json:"returnData,omitempty"`
}

type Account struct {
	Executable bool     `json:"executable"`
	Owner      string   `json:"owner"`
	Lamports   int      `json:"lamports"`
	Data       []string `json:"data"`
	RentEpoch  *big.Int `json:"rentEpoch,omitempty"`
}

type ReturnData struct {
	ProgramId string    `json:"programId"`
	Data      [2]string `json:"data"`
}

// SimulateBundle is an RPC method that simulates a Jito bundle – exclusively available to Jito-Solana validator.
func (c *Client) SimulateBundle(ctx context.Context, bundleParams SimulateBundleParams, simulationConfigs SimulateBundleConfig) (*SimulatedBundleResponse, error) {
	out := new(SimulatedBundleResponse)

	if len(bundleParams.EncodedTransactions) != len(simulationConfigs.PreExecutionAccountsConfigs) {
		return nil, errors.New("pre/post execution account config length must match bundle length")
	}

	err := c.JitoRpcConn.RPCCallForInto(ctx, out, "simulateBundle", []interface{}{bundleParams, simulationConfigs})
	return out, err
}

func (c *Client) AssembleBundle(transactions []types.Transaction) (*proto.Bundle, error) {
	packets, err := assemblePackets(transactions)
	if err != nil {
		return nil, err
	}

	return &proto.Bundle{Packets: packets, Header: nil}, nil
}

// GenerateTipInstruction is a function that generates a Solana tip instruction mandatory to broadcast a bundle to Jito.
func (c *Client) GenerateTipInstruction(tipAmount uint64, from, tipAccount solana.PublicKey) solana.Instruction {
	return system.NewTransferInstruction(tipAmount, from, tipAccount).Build()
}

// GenerateTipRandomAccountInstruction functions similarly to GenerateTipInstruction, but it selects a random tip account.
func (c *Client) GenerateTipRandomAccountInstruction(tipAmount uint64, from solana.PublicKey) (solana.Instruction, error) {
	tipAccount, err := c.GetRandomTipAccount()
	if err != nil {
		return nil, err
	}

	return system.NewTransferInstruction(tipAmount, from, solana.MustPublicKeyFromBase58(tipAccount)).Build(), nil
}

// assemblePackets is a function that converts a slice of transactions to a slice of protobuf packets.
func assemblePackets(transactions []types.Transaction) ([]*proto.Packet, error) {
	packets := make([]*proto.Packet, 0, len(transactions))

	for i, tx := range transactions {
		packet, err := pkg.ConvertTransactionToProtobufPacket(tx)
		if err != nil {
			return nil, fmt.Errorf("%d: error converting tx to proto packet [%w]", i, err)
		}

		packets = append(packets, &packet)
	}

	return packets, nil
}

type BundleRejectionError struct {
	Message string
}

func (e BundleRejectionError) Error() string {
	return e.Message
}

func NewStateAuctionBidRejectedError(auction string, tip uint64) error {
	return BundleRejectionError{
		Message: fmt.Sprintf("bundle lost state auction, auction: %s, tip %d lamports", auction, tip),
	}
}

func NewWinningBatchBidRejectedError(auction string, tip uint64) error {
	return BundleRejectionError{
		Message: fmt.Sprintf("bundle won state auction but failed global auction, auction %s, tip %d lamports", auction, tip),
	}
}

func NewSimulationFailureError(tx string, message string) error {
	return BundleRejectionError{
		Message: fmt.Sprintf("bundle simulation failure on tx %s, message: %s", tx, message),
	}
}

func NewInternalError(message string) error {
	return BundleRejectionError{
		Message: fmt.Sprintf("internal error %s", message),
	}
}

func NewDroppedBundle(message string) error {
	return BundleRejectionError{
		Message: fmt.Sprintf("bundle dropped %s", message),
	}
}
