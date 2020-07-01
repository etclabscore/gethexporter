package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/patrickmn/go-cache"
)

type etherbaseTransactionsT struct {
	base common.Address
	nTxs int
}

var (
	eth               *ethclient.Client
	geth              *GethInfo
	delay             int
	watchingAddresses string

	started time.Time

	blockCount            int64
	blockCountConsecutive int64
	blockSkippedCount     uint64
	transactionCount      int64
	transactionsZeroGas   uint64
	bigZero               = big.NewInt(0)
	addresses             map[string]Address
	etherbaseBalanceM     map[string]*big.Int
	//etherbaseBlocks            map[common.Address]int
	//etherbaseBlocksRatio       map[common.Address]float64
	//etherbaseBlockTimeDeltaMS  map[common.Address]uint64
	//etherbaseTransactions      map[common.Address]int
	//etherbaseTransactionsRatio map[common.Address]float64

	//etherbaseBlockTimeDeltaCache = cache.New(15*24*time.Hour, 10*time.Minute)
	etherbaseCounts       = cache.New(15*24*time.Hour, 10*time.Minute)
	etherbaseWinStreaking []common.Address

	etherbaseBlocks10    map[common.Address]int
	etherbaseBlocks100   map[common.Address]int
	etherbaseBlocks1000  map[common.Address]int
	etherbaseBlocks10000 map[common.Address]int

	etherbaseTransactions100   map[common.Address]int
	etherbaseTransactions1000  map[common.Address]int
	etherbaseTransactions10000 map[common.Address]int

	mu = new(sync.Mutex)
)

var etherbaseBlocksSl = []common.Address{}
var etherbaseTransactionsSl = []etherbaseTransactionsT{}

func init() {
	geth = new(GethInfo)
	addresses = make(map[string]Address)
	geth.TotalEthTransferred = big.NewInt(0)

	started = time.Now()

	//etherbaseBlocks = make(map[common.Address]int)
	//etherbaseBlocksRatio = make(map[common.Address]float64)
	//etherbaseBlockTimeDeltaMS = make(map[common.Address]uint64)

	//etherbaseTransactions = make(map[common.Address]int)
	//etherbaseTransactionsRatio = make(map[common.Address]float64)
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
	MaxEthTransferred      *big.Int
	MinEthTransferred      *big.Int
	MedEthTransferred      *big.Int
	MeanEthTransferred     *big.Int
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
	GasPriceMin            *big.Int
	TransactionNonceMean   uint64
	TransactionNonceMedian uint64
	BlockTimeDelta         uint64
}

type Address struct {
	Balance *big.Int
	Address string
	Nonce   uint64
}

func mustInitEthClient(err error) {
	for ; eth == nil || err != nil; eth, err = ethclient.Dial(geth.GethServer) {
		log.Println("ethclient", "eth", eth, "err", err)
		log.Println("re-attempting in 5s...")
		time.Sleep(5 * time.Second)
	}
}

func main() {
	var err error
	defer eth.Close()
	geth.GethServer = os.Getenv("GETH")
	if geth.GethServer == "" {
		panic("empty geth server address")
	}
	watchingAddresses = os.Getenv("ADDRESSES")
	delay, _ = strconv.Atoi(os.Getenv("DELAY"))
	if delay == 0 {
		delay = 500
	}
	metricsAddr := ":6061"
	if got := os.Getenv("METRICSADDR"); got != "" {
		metricsAddr = got
	}

	log.Printf("Connecting to Ethereum node: %v\n", geth.GethServer)

	mustInitEthClient(err)
	for ; geth.CurrentBlock == nil || err != nil; geth.CurrentBlock, err = eth.BlockByNumber(context.Background(), nil) {
		log.Println("init client current block", "block", geth.CurrentBlock, err)
		log.Println("re-attempting in 5s...")
		time.Sleep(5 * time.Second)
	}

	log.Println("got initial current block", geth.CurrentBlock.Number(), geth.CurrentBlock.Hash().Hex())

	go Routine()

	log.Printf("Geth Exporter running on %s/metrics\n", metricsAddr)

	http.HandleFunc("/metrics", MetricsHttp)
	err = http.ListenAndServe(metricsAddr, nil)
	if err != nil {
		panic(err)
	}
}

func Routine() error {
	var parentBlock = geth.CurrentBlock
	ctx := context.Background()

	for {
		latestBlock, err := eth.BlockByNumber(ctx, nil)
		if err != nil {
			log.Printf("issue with reponse from geth server: %v\n", err)
			mustInitEthClient(err)
			return Routine()
		}
		if parentBlock.NumberU64() < latestBlock.NumberU64() {

			// Backfill blocks that occurred within the interval.
			for i := parentBlock.NumberU64() + 1; i < latestBlock.NumberU64(); i++ {
				geth.CurrentBlock, _ = eth.BlockByNumber(ctx, big.NewInt(int64(i)))
				CalculateBlockTotals(geth.CurrentBlock)
				onBlock(ctx, parentBlock)
				parentBlock = geth.CurrentBlock
			}

			geth.CurrentBlock = latestBlock
			CalculateBlockTotals(geth.CurrentBlock)
			onBlock(ctx, parentBlock)
			parentBlock = geth.CurrentBlock
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
}

func onBlock(ctx context.Context, lastBlock *types.Block) {
	t1 := time.Now()

	geth.SugGasPrice, _ = eth.SuggestGasPrice(ctx)
	geth.PendingTx, _ = eth.PendingTransactionCount(ctx)
	geth.NetworkId, _ = eth.NetworkID(ctx)
	geth.ChainId, _ = eth.ChainID(ctx)
	geth.Sync, _ = eth.SyncProgress(ctx)

	if lastBlock == nil || geth.CurrentBlock.NumberU64() > lastBlock.NumberU64() {
		log.Printf("Received block #%v with %v transactions (%v)\n", geth.CurrentBlock.NumberU64(), len(geth.CurrentBlock.Transactions()), geth.CurrentBlock.Hash().String())

		geth.LastBlockUpdate = time.Now()
		geth.LoadTime = time.Now().Sub(t1).Seconds()

		blockCount++
		transactionCount += int64(geth.CurrentBlock.Transactions().Len())
		for _, t := range geth.CurrentBlock.Transactions() {
			if t.GasPrice().Cmp(bigZero) == 0 {
				transactionsZeroGas++
			}
		}

		if lastBlock != nil {
			blockSkippedCount += geth.CurrentBlock.NumberU64() - 1 - lastBlock.NumberU64()

			if geth.CurrentBlock.NumberU64() == lastBlock.NumberU64()+1 {

				blockCountConsecutive++

				geth.BlockTimeDelta = geth.CurrentBlock.Time() - lastBlock.Time()

				if len(etherbaseWinStreaking) == 0 || etherbaseWinStreaking[0] != geth.CurrentBlock.Coinbase() {
					etherbaseWinStreaking = []common.Address{geth.CurrentBlock.Coinbase()}
				} else {
					etherbaseWinStreaking = append(etherbaseWinStreaking, geth.CurrentBlock.Coinbase())
				}

			} else {
				blockCountConsecutive = 0
				etherbaseWinStreaking = []common.Address{}
			}
		}

		calculateEtherbaseCounters(geth.CurrentBlock, lastBlock)

		// Update the winning etherbase in the ttl map
		etherbaseStr := geth.CurrentBlock.Coinbase().Hex()

		// Increment etherbase win tallies
		if it, ok := etherbaseCounts.Get(etherbaseStr); ok {
			etherbaseCounts.SetDefault(etherbaseStr, it.(int)+1)
		} else {
			etherbaseCounts.SetDefault(etherbaseStr, 1)
		}

		its := etherbaseCounts.Items()
		mu.Lock()
		etherbaseBalanceM = make(map[string]*big.Int, len(its))
		mu.Unlock()
		for k := range its {
			b, err := eth.BalanceAt(ctx, common.HexToAddress(k), geth.CurrentBlock.Number())
			if err == nil {
				mu.Lock()
				etherbaseBalanceM[k] = b
				mu.Unlock()
			} else {
				log.Println("error getting etherbase balance", err, etherbaseStr)
			}
		}
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
			mu.Lock()
			addresses[a] = address
			mu.Unlock()
		}
	}

}

func CalculateBlockTotals(block *types.Block) {
	geth.TotalEthTransferred = big.NewInt(0)

	geth.MinEthTransferred = big.NewInt(-1)
	geth.MaxEthTransferred = big.NewInt(0)
	geth.MedEthTransferred = big.NewInt(0)
	transferVals := []*big.Int{}
	geth.MeanEthTransferred = big.NewInt(0)

	geth.ContractsCreated = 0
	geth.TokenTransfers = 0
	geth.EthTransfers = 0

	geth.GasSpent = big.NewInt(0)

	geth.GasPriceMean = big.NewInt(0)
	geth.GasPriceMedian = big.NewInt(0)
	geth.GasPriceMin = big.NewInt(-1)

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
		transferVals = append(transferVals, b.Value())

		if b.Value().Cmp(geth.MaxEthTransferred) > 0 {
			geth.MaxEthTransferred.Set(b.Value())
		}
		if geth.MinEthTransferred.Sign() < 0 || b.Value().Cmp(geth.MinEthTransferred) < 0 {
			geth.MinEthTransferred.Set(b.Value())
		}

		geth.GasSpent.Add(geth.GasSpent, new(big.Int).Mul(b.GasPrice(), new(big.Int).SetUint64(b.Gas())))
		gasPriceSum.Add(gasPriceSum, b.GasPrice())
		gasPrices = append(gasPrices, b.GasPrice())

		if geth.GasPriceMin.Sign() < 0 || b.GasPrice().Cmp(geth.GasPriceMin) < 0 {
			geth.GasPriceMin.Set(b.GasPrice())
		}

		txNonces = append(txNonces, b.Nonce())
		txNonceSum += b.Nonce()
	}

	if block.Transactions().Len() > 0 {
		geth.GasPriceMean.Div(gasPriceSum, new(big.Int).SetUint64(uint64(block.Transactions().Len())))
		geth.TransactionNonceMean = txNonceSum / uint64(block.Transactions().Len())
		geth.MeanEthTransferred.Div(geth.TotalEthTransferred, big.NewInt(int64(block.Transactions().Len())))
	}

	sort.Slice(gasPrices, func(i, j int) bool {
		return gasPrices[i].Cmp(gasPrices[j]) < 0
	})
	if len(gasPrices) >= 2 {
		geth.GasPriceMedian = gasPrices[len(gasPrices)/2]
	} else if len(gasPrices) == 1 {
		geth.GasPriceMedian = gasPrices[0]
	}

	sort.Slice(transferVals, func(i, j int) bool {
		return transferVals[i].Cmp(transferVals[j]) < 0
	})
	if len(transferVals) >= 2 {
		geth.MedEthTransferred = transferVals[len(transferVals)/2]
	} else if len(transferVals) == 1 {
		geth.MedEthTransferred = transferVals[0]
	}

	if len(txNonces) >= 2 {
		geth.TransactionNonceMedian = txNonces[len(txNonces)/2]
	} else if len(txNonces) == 1 {
		geth.TransactionNonceMedian = txNonces[0]
	}

	size := strings.Split(block.Size().String(), " ")
	geth.BlockSize = stringToFloat(size[0]) * 1000
}

func calculateEtherbaseCounters(block, lastBlock *types.Block) {

	txLen := block.Transactions().Len()
	addr := block.Coinbase()

	for len(etherbaseBlocksSl) >= 10000 {
		// pop
		etherbaseBlocksSl = etherbaseBlocksSl[:len(etherbaseBlocksSl)-1]
	}
	// push front
	etherbaseBlocksSl = append([]common.Address{addr}, etherbaseBlocksSl...)

	for len(etherbaseTransactionsSl) >= 10000 {
		// pop
		etherbaseTransactionsSl = etherbaseTransactionsSl[:len(etherbaseTransactionsSl)-1]
	}
	// push front
	etherbaseTransactionsSl = append([]etherbaseTransactionsT{{base: addr, nTxs: txLen}}, etherbaseTransactionsSl...)

	mu.Lock()
	defer mu.Unlock()

	etherbaseBlocks10 = make(map[common.Address]int, 10)
	etherbaseBlocks100 = make(map[common.Address]int, 100)
	etherbaseBlocks1000 = make(map[common.Address]int, 1000)
	etherbaseBlocks10000 = make(map[common.Address]int, 10000)

	etherbaseTransactions100 = make(map[common.Address]int, 100)
	etherbaseTransactions1000 = make(map[common.Address]int, 1000)
	etherbaseTransactions10000 = make(map[common.Address]int, 10000)

	for i, v := range etherbaseBlocksSl {
		if i <= 10-1 {
			if _, ok := etherbaseBlocks10[v]; !ok {
				etherbaseBlocks10[v] = 1
			} else {
				etherbaseBlocks10[v]++
			}
		}
		if i <= 100-1 {
			if _, ok := etherbaseBlocks100[v]; !ok {
				etherbaseBlocks100[v] = 1
			} else {
				etherbaseBlocks100[v]++
			}
		}
		if i <= 1000-1 {
			if _, ok := etherbaseBlocks1000[v]; !ok {
				etherbaseBlocks1000[v] = 1
			} else {
				etherbaseBlocks1000[v]++
			}
		}
		if i <= 10000-1 {
			if _, ok := etherbaseBlocks10000[v]; !ok {
				etherbaseBlocks10000[v] = 1
			} else {
				etherbaseBlocks10000[v]++
			}
		}
	}

	for i, v := range etherbaseTransactionsSl {
		if i <= 100-1 {
			if _, ok := etherbaseTransactions100[v.base]; !ok {
				etherbaseTransactions100[v.base] = v.nTxs
			} else {
				etherbaseTransactions100[v.base] += v.nTxs
			}
		}
		if i <= 1000-1 {
			if _, ok := etherbaseTransactions1000[v.base]; !ok {
				etherbaseTransactions1000[v.base] = v.nTxs
			} else {
				etherbaseTransactions1000[v.base] += v.nTxs
			}
		}
		if i <= 10000-1 {
			if _, ok := etherbaseTransactions10000[v.base]; !ok {
				etherbaseTransactions10000[v.base] = v.nTxs
			} else {
				etherbaseTransactions10000[v.base] += v.nTxs
			}
		}
	}
}

//
// HTTP response handler for /metrics
func MetricsHttp(w http.ResponseWriter, r *http.Request) {
	log.Println("metrics handler", r.Method, r.Proto, r.Host, r.RequestURI)
	var allOut []string
	block := geth.CurrentBlock
	if block == nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("issue receiving block from URL: %v", geth.GethServer)))
		return
	}

	allOut = append(allOut, fmt.Sprintf("geth_block_number %v", block.NumberU64()))

	allOut = append(allOut, fmt.Sprintf("geth_block_delta_subjective_seconds %0.2f", time.Now().Sub(geth.LastBlockUpdate).Seconds()))
	allOut = append(allOut, fmt.Sprintf("geth_block_delta_seconds %v", geth.BlockTimeDelta))

	allOut = append(allOut, fmt.Sprintf("geth_block_transactions_count %v", block.Transactions().Len()))
	allOut = append(allOut, fmt.Sprintf("geth_block_transactions_sum_value_transfer %v", ToEther(geth.TotalEthTransferred)))

	allOut = append(allOut, fmt.Sprintf("geth_block_transactions_value_min %v", ToEther(geth.MinEthTransferred)))
	allOut = append(allOut, fmt.Sprintf("geth_block_transactions_value_max %v", ToEther(geth.MaxEthTransferred)))
	allOut = append(allOut, fmt.Sprintf("geth_block_transactions_value_median %v", ToEther(geth.MedEthTransferred)))
	allOut = append(allOut, fmt.Sprintf("geth_block_transactions_value_mean %v", ToEther(geth.MeanEthTransferred)))

	allOut = append(allOut, fmt.Sprintf("geth_block_nonce %v", block.Nonce()))
	allOut = append(allOut, fmt.Sprintf("geth_block_difficulty %v", block.Difficulty()))
	allOut = append(allOut, fmt.Sprintf("geth_block_uncles_count %v", len(block.Uncles())))
	allOut = append(allOut, fmt.Sprintf("geth_block_size_bytes %v", geth.BlockSize))
	allOut = append(allOut, fmt.Sprintf("geth_block_etherbase{address=\"%s\"} %d", block.Coinbase().Hex(), 1))

	allOut = append(allOut, fmt.Sprintf("geth_block_gas_used %v", block.GasUsed()))
	allOut = append(allOut, fmt.Sprintf("geth_block_gas_limit %v", block.GasLimit()))
	allOut = append(allOut, fmt.Sprintf("geth_block_gas_spent %v", geth.GasSpent))
	allOut = append(allOut, fmt.Sprintf("geth_block_gas_price_mean %v", geth.GasPriceMean))
	allOut = append(allOut, fmt.Sprintf("geth_block_gas_price_median %v", geth.GasPriceMedian))
	allOut = append(allOut, fmt.Sprintf("geth_block_gas_price_min %v", geth.GasPriceMin))

	allOut = append(allOut, fmt.Sprintf("geth_block_transaction_nonce_mean %v", geth.TransactionNonceMean))
	allOut = append(allOut, fmt.Sprintf("geth_block_transaction_nonce_median %v", geth.TransactionNonceMedian))

	allOut = append(allOut, fmt.Sprintf("geth_block_contract_create_count %v", geth.ContractsCreated))
	allOut = append(allOut, fmt.Sprintf("geth_block_token_transfer_count %v", geth.TokenTransfers))
	allOut = append(allOut, fmt.Sprintf("geth_block_value_transfer_count %v", geth.EthTransfers))

	allOut = append(allOut, fmt.Sprintf("geth_txpool_pending_count %v", geth.PendingTx))

	allOut = append(allOut, fmt.Sprintf("geth_network_id %v", geth.NetworkId))
	allOut = append(allOut, fmt.Sprintf("geth_network_chain_id %v", geth.ChainId))

	allOut = append(allOut, fmt.Sprintf("geth_api_suggested_gas_price %v", geth.SugGasPrice))

	allOut = append(allOut, fmt.Sprintf("geth_load_time_seconds %0.4f", geth.LoadTime))
	allOut = append(allOut, fmt.Sprintf("geth_exporter_uptime %0f", time.Now().Sub(started).Seconds()))

	if geth.Sync != nil {
		allOut = append(allOut, fmt.Sprintf("geth_sync_known_states %v", int(geth.Sync.KnownStates)))
		allOut = append(allOut, fmt.Sprintf("geth_sync_highest_block %v", int(geth.Sync.HighestBlock)))
		allOut = append(allOut, fmt.Sprintf("geth_sync_pulled_states %v", int(geth.Sync.PulledStates)))
	}

	allOut = append(allOut, fmt.Sprintf("geth_blocks_total %v", blockCount))
	allOut = append(allOut, fmt.Sprintf("geth_blocks_consecutive_total %v", blockCountConsecutive)) // not actually a total, its a gauge
	allOut = append(allOut, fmt.Sprintf("geth_blocks_skipped_total %v", blockSkippedCount))
	allOut = append(allOut, fmt.Sprintf("geth_transactions_total %v", transactionCount))
	allOut = append(allOut, fmt.Sprintf("geth_transactions_zerogas_total %v", transactionsZeroGas))

	mu.Lock()
	for _, v := range addresses {
		allOut = append(allOut, fmt.Sprintf("geth_address_balance{address=\"%v\"} %v", v.Address, ToEther(v.Balance).String()))
		allOut = append(allOut, fmt.Sprintf("geth_address_nonce{address=\"%v\"} %v", v.Address, v.Nonce))
	}

	for k, v := range etherbaseCounts.Items() {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_count{address=\"%s\"} %v", k, v.Object.(int)))
	}

	if len(etherbaseWinStreaking) > 0 {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_streak{address=\"%s\"} %d", etherbaseWinStreaking[0].Hex(), len(etherbaseWinStreaking)))
	}

	for k, v := range etherbaseBlocks10 {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_block_10_total{address=\"%s\"} %v", k.Hex(), v))
	}
	for k, v := range etherbaseBlocks100 {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_block_100_total{address=\"%s\"} %v", k.Hex(), v))
	}
	for k, v := range etherbaseBlocks1000 {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_block_1000_total{address=\"%s\"} %v", k.Hex(), v))
	}
	for k, v := range etherbaseBlocks10000 {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_block_10000_total{address=\"%s\"} %v", k.Hex(), v))
	}

	for k, v := range etherbaseTransactions100 {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_transaction_100_total{address=\"%s\"} %v", k.Hex(), v))
	}
	for k, v := range etherbaseTransactions1000 {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_transaction_1000_total{address=\"%s\"} %v", k.Hex(), v))
	}
	for k, v := range etherbaseTransactions10000 {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_transaction_10000_total{address=\"%s\"} %v", k.Hex(), v))
	}

	for k, v := range etherbaseBalanceM {
		allOut = append(allOut, fmt.Sprintf("geth_etherbase_balance{address=\"%s\"} %v", k, ToEther(v)))
	}
	mu.Unlock()

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
	if o == nil {
		return big.NewFloat(0)
	}
	pul, int := big.NewFloat(0), big.NewFloat(0)
	int.SetInt(o)
	pul.Mul(big.NewFloat(0.000000000000000001), int)
	return pul
}
