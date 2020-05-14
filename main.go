package main

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"log"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	eth                        *ethclient.Client
	geth                       *GethInfo
	delay                      int
	watchingAddresses          string
	blockCount                 int
	transactionCount           int
	addresses                  map[string]Address
	etherbaseBlocks            map[common.Address]int
	etherbaseBlocksRatio       map[common.Address]float64
	etherbaseBlockTimeDeltaMS  map[common.Address]uint64
	etherbaseTransactions      map[common.Address]int
	etherbaseTransactionsRatio map[common.Address]float64
)

func init() {
	geth = new(GethInfo)
	addresses = make(map[string]Address)
	geth.TotalEthTransferred = big.NewInt(0)

	etherbaseBlocks = make(map[common.Address]int)
	etherbaseBlocksRatio = make(map[common.Address]float64)
	etherbaseBlockTimeDeltaMS = make(map[common.Address]uint64)

	etherbaseTransactions = make(map[common.Address]int)
	etherbaseTransactionsRatio = make(map[common.Address]float64)
}

type GethInfo struct {
	GethServer             string
	ContractsCreated       int64
	TokenTransfers         int64
	ContractCalls          int64
	EthTransfers           int64
	BlockSize              float64
	LoadTime               float64
	TotalEthTransferred    *big.Int
	CurrentBlock           *types.Block
	Sync                   *ethereum.SyncProgress
	LastBlockUpdate        time.Time
	SugGasPrice            *big.Int
	PendingTx              uint
	NetworkId              *big.Int
	ChainId                *big.Int
	GasSpent               *big.Int
	GasPriceMean           *big.Int
	GasPriceMedian         *big.Int
	TransactionNonceMean   uint64
	TransactionNonceMedian uint64
	BlockTimeDelta         uint64
}

type Address struct {
	Balance *big.Int
	Address string
	Nonce   uint64
}

func main() {
	var err error
	defer eth.Close()
	geth.GethServer = os.Getenv("GETH")
	watchingAddresses = os.Getenv("ADDRESSES")
	delay, _ = strconv.Atoi(os.Getenv("DELAY"))
	if delay == 0 {
		delay = 500
	}
	if geth.GethServer == "" {
		panic("empty geth server address")
	}
	log.Printf("Connecting to Ethereum node: %v\n", geth.GethServer)
	for ; eth == nil || err != nil ; eth, err = ethclient.Dial(geth.GethServer) {
		log.Println("ethclient", "eth", eth, "err", err)
		log.Println("re-attempting in 5s...")
		time.Sleep(5*time.Second)
	}
	geth.CurrentBlock, err = eth.BlockByNumber(context.TODO(), nil)
	if err != nil {
		panic(err)
	}

	go Routine()

	log.Printf("Geth Exporter running on http://localhost:9090/metrics\n")

	http.HandleFunc("/metrics", MetricsHttp)
	err = http.ListenAndServe(":6061", nil)
	if err != nil {
		panic(err)
	}
}

func CalculateBlockTotals(block *types.Block) {
	geth.TotalEthTransferred = big.NewInt(0)
	geth.ContractsCreated = 0
	geth.TokenTransfers = 0
	geth.EthTransfers = 0

	geth.GasSpent = big.NewInt(0)
	geth.GasPriceMean = big.NewInt(0)
	geth.GasPriceMedian = big.NewInt(0)
	gasPriceSum := big.NewInt(0)
	gasPrices := []*big.Int{}

	geth.TransactionNonceMedian = 0
	geth.TransactionNonceMean = 0
	txNonceSum := uint64(0)
	txNonces := []uint64{}

	for _, b := range block.Transactions() {

		if b.To() == nil {
			geth.ContractsCreated++
		}

		if len(b.Data()) >= 4 {
			method := hexutil.Encode(b.Data()[:4])
			if method == "0xa9059cbb" {
				geth.TokenTransfers++
			}
		}

		if b.Value().Sign() == 1 {
			geth.EthTransfers++
		}

		geth.TotalEthTransferred.Add(geth.TotalEthTransferred, b.Value())

		geth.GasSpent.Add(geth.GasSpent, new(big.Int).Mul(b.GasPrice(), new(big.Int).SetUint64(b.Gas())))
		gasPriceSum.Add(gasPriceSum, b.GasPrice())
		gasPrices = append(gasPrices, b.GasPrice())

		txNonces = append(txNonces, b.Nonce())
		txNonceSum += b.Nonce()
	}

	if block.Transactions().Len() > 0 {
		geth.GasPriceMean.Div(gasPriceSum, new(big.Int).SetUint64(uint64(block.Transactions().Len())))
		geth.TransactionNonceMean = txNonceSum / uint64(block.Transactions().Len())
	}

	if len(gasPrices) >= 2 {
		geth.GasPriceMedian = gasPrices[len(gasPrices)/2]
	} else if len(gasPrices) == 1 {
		geth.GasPriceMedian = gasPrices[0]
	}

	if len(txNonces) >= 2 {
		geth.TransactionNonceMedian = txNonces[len(txNonces)/2]
	} else if len(txNonces) == 1 {
		geth.TransactionNonceMedian = txNonces[0]
	}

	size := strings.Split(geth.CurrentBlock.Size().String(), " ")
	geth.BlockSize = stringToFloat(size[0]) * 1000
}

func calculateEtherbaseCounters(block, lastBlock *types.Block) {
	blockCount++

	txLen := block.Transactions().Len()
	transactionCount += txLen

	addr := block.Coinbase()

	if lastBlock != nil {
		etherbaseBlockTimeDeltaMS[addr] = block.Time() - lastBlock.Time()
	}

	if _, ok := etherbaseBlocks[addr]; !ok {
		etherbaseBlocks[addr] = 1
		etherbaseBlocksRatio[addr] = 1 / float64(blockCount)
	} else {
		etherbaseBlocks[addr]++
		etherbaseBlocksRatio[addr] = float64(etherbaseBlocks[addr]) / float64(blockCount)
	}

	if _, ok := etherbaseTransactions[addr]; !ok {
		etherbaseTransactions[addr] = txLen
		etherbaseTransactionsRatio[addr] = float64(txLen) / float64(transactionCount)
	} else {
		etherbaseTransactions[addr] += txLen
		etherbaseTransactionsRatio[addr] = float64(etherbaseTransactions[addr]) / float64(transactionCount)
	}
}

func Routine() {
	var lastBlock *types.Block
	ctx := context.Background()
	for {
		t1 := time.Now()
		var err error
		geth.CurrentBlock, err = eth.BlockByNumber(ctx, nil)
		if err != nil {
			log.Printf("issue with reponse from geth server: %v\n", geth.CurrentBlock)
			time.Sleep(time.Duration(delay) * time.Millisecond)
			continue
		}
		geth.SugGasPrice, _ = eth.SuggestGasPrice(ctx)
		geth.PendingTx, _ = eth.PendingTransactionCount(ctx)
		geth.NetworkId, _ = eth.NetworkID(ctx)
		geth.ChainId, _ = eth.ChainID(ctx)
		geth.Sync, _ = eth.SyncProgress(ctx)

		if lastBlock == nil || geth.CurrentBlock.NumberU64() > lastBlock.NumberU64() {
			log.Printf("Received block #%v with %v transactions (%v)\n", geth.CurrentBlock.NumberU64(), len(geth.CurrentBlock.Transactions()), geth.CurrentBlock.Hash().String())

			geth.LastBlockUpdate = time.Now()
			geth.LoadTime = time.Now().Sub(t1).Seconds()
			if lastBlock != nil {
				geth.BlockTimeDelta = geth.CurrentBlock.Time() - lastBlock.Time()
			}

			calculateEtherbaseCounters(geth.CurrentBlock, lastBlock)
		}

		if watchingAddresses != "" {
			for _, a := range strings.Split(watchingAddresses, ",") {
				addr := common.HexToAddress(a)
				balance, _ := eth.BalanceAt(ctx, addr, geth.CurrentBlock.Number())
				nonce, _ := eth.NonceAt(ctx, addr, geth.CurrentBlock.Number())
				address := Address{
					Address: addr.String(),
					Balance: balance,
					Nonce:   nonce,
				}
				addresses[a] = address
			}
		}

		lastBlock = geth.CurrentBlock
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
}

//
// HTTP response handler for /metrics
func MetricsHttp(w http.ResponseWriter, r *http.Request) {
	var allOut []string
	block := geth.CurrentBlock
	if block == nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("issue receiving block from URL: %v", geth.GethServer)))
		return
	}
	CalculateBlockTotals(block)

	allOut = append(allOut, fmt.Sprintf("geth_block %v", block.NumberU64()))

	allOut = append(allOut, fmt.Sprintf("geth_seconds_last_block_subjective %0.2f", time.Now().Sub(geth.LastBlockUpdate).Seconds()))
	allOut = append(allOut, fmt.Sprintf("geth_seconds_last_block_reported %v", geth.BlockTimeDelta))

	allOut = append(allOut, fmt.Sprintf("geth_block_transactions %v", block.Transactions().Len()))
	allOut = append(allOut, fmt.Sprintf("geth_block_value_transferred %v", ToEther(geth.TotalEthTransferred)))
	allOut = append(allOut, fmt.Sprintf("geth_block_nonce %v", block.Nonce()))
	allOut = append(allOut, fmt.Sprintf("geth_block_difficulty %v", block.Difficulty()))
	allOut = append(allOut, fmt.Sprintf("geth_block_uncles %v", len(block.Uncles())))
	allOut = append(allOut, fmt.Sprintf("geth_block_size_bytes %v", geth.BlockSize))

	allOut = append(allOut, fmt.Sprintf("geth_suggested_gas_price %v", geth.SugGasPrice))
	allOut = append(allOut, fmt.Sprintf("geth_block_gas_used %v", block.GasUsed()))
	allOut = append(allOut, fmt.Sprintf("geth_block_gas_limit %v", block.GasLimit()))
	allOut = append(allOut, fmt.Sprintf("geth_block_gas_spent %v", geth.GasSpent))
	allOut = append(allOut, fmt.Sprintf("geth_block_gas_price_mean %v", geth.GasPriceMean))
	allOut = append(allOut, fmt.Sprintf("geth_block_gas_price_median %v", geth.GasPriceMedian))

	allOut = append(allOut, fmt.Sprintf("geth_block_tx_nonce_mean %v", geth.TransactionNonceMean))
	allOut = append(allOut, fmt.Sprintf("geth_block_tx_nonce_median %v", geth.TransactionNonceMedian))

	allOut = append(allOut, fmt.Sprintf("geth_pending_transactions %v", geth.PendingTx))
	allOut = append(allOut, fmt.Sprintf("geth_network_id %v", geth.NetworkId))
	allOut = append(allOut, fmt.Sprintf("geth_chain_id %v", geth.ChainId))
	allOut = append(allOut, fmt.Sprintf("geth_contracts_created %v", geth.ContractsCreated))
	allOut = append(allOut, fmt.Sprintf("geth_token_transfers %v", geth.TokenTransfers))
	allOut = append(allOut, fmt.Sprintf("geth_eth_transfers %v", geth.EthTransfers))
	allOut = append(allOut, fmt.Sprintf("geth_load_time %0.4f", geth.LoadTime))

	if geth.Sync != nil {
		allOut = append(allOut, fmt.Sprintf("geth_known_states %v", int(geth.Sync.KnownStates)))
		allOut = append(allOut, fmt.Sprintf("geth_highest_block %v", int(geth.Sync.HighestBlock)))
		allOut = append(allOut, fmt.Sprintf("geth_pulled_states %v", int(geth.Sync.PulledStates)))
	}

	for _, v := range addresses {
		allOut = append(allOut, fmt.Sprintf("geth_address_balance{address=\"%v\"} %v", v.Address, ToEther(v.Balance).String()))
		allOut = append(allOut, fmt.Sprintf("geth_address_nonce{address=\"%v\"} %v", v.Address, v.Nonce))
	}
	for k, v := range etherbaseBlocks {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_block{address=\"%s\"} %v", k.Hex(), v))
	}
	for k, v := range etherbaseBlocksRatio {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_block_ratio{address=\"%s\"} %0.2f", k.Hex(), v))
	}
	for k, v := range etherbaseBlockTimeDeltaMS {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_block_time_delta{address=\"%s\"} %v", k.Hex(), v))
	}

	for k, v := range etherbaseTransactions {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_transaction{address=\"%s\"} %v", k.Hex(), v))
	}
	for k, v := range etherbaseTransactionsRatio {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_transaction_ratio{address=\"%s\"} %0.2f", k.Hex(), v))
	}

	w.Write([]byte(strings.Join(allOut, "\n")))
}

// stringToFloat will simply convert a string to a float
func stringToFloat(s string) float64 {
	amount, _ := strconv.ParseFloat(s, 10)
	return amount
}

//
// CONVERTS WEI TO ETH
func ToEther(o *big.Int) *big.Float {
	pul, int := big.NewFloat(0), big.NewFloat(0)
	int.SetInt(o)
	pul.Mul(big.NewFloat(0.000000000000000001), int)
	return pul
}
