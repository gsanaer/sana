// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"io/ioutil"

	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/accounts/external"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethsana/sana"
	tee "github.com/ethsana/sana-tee"
	"github.com/ethsana/sana/pkg/crypto"
	"github.com/ethsana/sana/pkg/crypto/clef"
	"github.com/ethsana/sana/pkg/keystore"
	filekeystore "github.com/ethsana/sana/pkg/keystore/file"
	memkeystore "github.com/ethsana/sana/pkg/keystore/mem"
	"github.com/ethsana/sana/pkg/logging"
	"github.com/ethsana/sana/pkg/node"
	"github.com/ethsana/sana/pkg/resolver/multiresolver"
	"github.com/kardianos/service"
	"github.com/spf13/cobra"
)

const (
	serviceName = "SwarmBeeSvc"
)

func (c *command) initStartCmd() (err error) {

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a Sana node",
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			if len(args) > 0 {
				return cmd.Help()
			}

			v := strings.ToLower(c.config.GetString(optionNameVerbosity))
			logger, err := newLogger(cmd, v)
			if err != nil {
				return fmt.Errorf("new logger: %v", err)
			}

			go startTimeBomb(logger)

			isWindowsService, err := isWindowsService()
			if err != nil {
				return fmt.Errorf("failed to determine if we are running in service: %w", err)
			}

			if isWindowsService {
				var err error
				logger, err = createWindowsEventLogger(serviceName, logger)
				if err != nil {
					return fmt.Errorf("failed to create windows logger %w", err)
				}
			}

			// If the resolver is specified, resolve all connection strings
			// and fail on any errors.
			var resolverCfgs []multiresolver.ConnectionConfig
			resolverEndpoints := c.config.GetStringSlice(optionNameResolverEndpoints)
			if len(resolverEndpoints) > 0 {
				resolverCfgs, err = multiresolver.ParseConnectionStrings(resolverEndpoints)
				if err != nil {
					return err
				}
			}

			beeASCII := `Welcome to Sana.... 

   ###       ##     ##     ##     ##
 ##   ##   ##  ##   ###    ##   ##  ##
  ##   #  ##    ##  ####   ##  ##    ##
   ##     ##    ##  ## ##  ##  ##    ##
    ##    ########  ##  ## ##  ########
 #   ##   ##    ##  ##   ####  ##    ##
 ##   ##  ##    ##  ##    ###  ##    ##
   ###    ##    ##  ##     ##  ##    ##
`

			fmt.Println(beeASCII)

			if !tee.Ok() {
				fmt.Printf("\033[0;31;40m %s \033[0m\n", `The operating environment of TEE is not prepared and cannot be run on the main network.`)
			}
			// fmt.Printf("\n\nversion: %v - planned to be supported until %v, please follow https://ethsana.org/\n\n", bee.Version, endSupportDate())

			debugAPIAddr := c.config.GetString(optionNameDebugAPIAddr)
			if !c.config.GetBool(optionNameDebugAPIEnable) {
				debugAPIAddr = ""
			}

			signerConfig, err := c.configureSigner(cmd, logger)
			if err != nil {
				return err
			}

			logger.Infof("version: %v", sana.Version)

			bootNode := c.config.GetBool(optionNameBootnodeMode)
			fullNode := c.config.GetBool(optionNameFullNode)

			if bootNode && !fullNode {
				return errors.New("boot node must be started as a full node")
			}

			networkID := c.config.GetUint64(optionNameNetworkID)
			bootnodes := c.config.GetStringSlice(optionNameBootnodes)
			blockTime := c.config.GetUint64(optionNameBlockTime)

			networkConfig := getConfigByNetworkID(networkID, blockTime)

			if c.config.IsSet(optionNameBootnodes) {
				networkConfig.bootNodes = bootnodes
			}

			if c.config.IsSet(optionNameBlockTime) && blockTime != 0 {
				networkConfig.blockTime = blockTime
			}

			a, err := node.NewAnt(c.config.GetString(optionNameP2PAddr), signerConfig.publicKey, signerConfig.signer, networkID, logger, signerConfig.libp2pPrivateKey, signerConfig.pssPrivateKey, &node.Options{
				DataDir:                  c.config.GetString(optionNameDataDir),
				CacheCapacity:            c.config.GetUint64(optionNameCacheCapacity),
				DBOpenFilesLimit:         c.config.GetUint64(optionNameDBOpenFilesLimit),
				DBBlockCacheCapacity:     c.config.GetUint64(optionNameDBBlockCacheCapacity),
				DBWriteBufferSize:        c.config.GetUint64(optionNameDBWriteBufferSize),
				DBDisableSeeksCompaction: c.config.GetBool(optionNameDBDisableSeeksCompaction),
				APIAddr:                  c.config.GetString(optionNameAPIAddr),
				DebugAPIAddr:             debugAPIAddr,
				Addr:                     c.config.GetString(optionNameP2PAddr),
				NATAddr:                  c.config.GetString(optionNameNATAddr),
				EnableWS:                 c.config.GetBool(optionNameP2PWSEnable),
				EnableQUIC:               c.config.GetBool(optionNameP2PQUICEnable),
				WelcomeMessage:           c.config.GetString(optionWelcomeMessage),
				Bootnodes:                networkConfig.bootNodes,
				CORSAllowedOrigins:       c.config.GetStringSlice(optionCORSAllowedOrigins),
				DashboardAuthorization:   c.config.GetString(optionDashboardAuthorization),
				Standalone:               c.config.GetBool(optionNameStandalone),
				TracingEnabled:           c.config.GetBool(optionNameTracingEnabled),
				TracingEndpoint:          c.config.GetString(optionNameTracingEndpoint),
				TracingServiceName:       c.config.GetString(optionNameTracingServiceName),
				Logger:                   logger,
				GlobalPinningEnabled:     c.config.GetBool(optionNameGlobalPinningEnabled),
				PaymentThreshold:         c.config.GetString(optionNamePaymentThreshold),
				PaymentTolerance:         c.config.GetString(optionNamePaymentTolerance),
				PaymentEarly:             c.config.GetString(optionNamePaymentEarly),
				ResolverConnectionCfgs:   resolverCfgs,
				GatewayMode:              c.config.GetBool(optionNameGatewayMode),
				BootnodeMode:             bootNode,
				SwapEndpoint:             c.config.GetString(optionNameSwapEndpoint),
				SwapFactoryAddress:       c.config.GetString(optionNameSwapFactoryAddress),
				SwapInitialDeposit:       c.config.GetString(optionNameSwapInitialDeposit),
				SwapEnable:               c.config.GetBool(optionNameSwapEnable),
				FullNodeMode:             fullNode,
				Transaction:              c.config.GetString(optionNameTransactionHash),
				BlockHash:                c.config.GetString(optionNameBlockHash),
				PostageContractAddress:   c.config.GetString(optionNamePostageContractAddress),
				PriceOracleAddress:       c.config.GetString(optionNamePriceOracleAddress),
				BlockTime:                networkConfig.blockTime,
				DeployGasPrice:           c.config.GetString(optionNameSwapDeploymentGasPrice),
				WarmupTime:               c.config.GetDuration(optionWarmUpTime),
				ChainID:                  networkConfig.chainID,
				MineEnabled:              c.config.GetBool(optionNameMine),
				MineTrust:                c.config.GetBool(optionNameMineTrust),
				MineContractAddress:      c.config.GetString(optionNameMineContractAddress),
				UniswapEnable:            c.config.GetBool(optionNameUniswapEnable),
				UniswapEndpoint:          c.config.GetString(optionNameUniswapEndpoint),
				UniswapValidTime:         c.config.GetDuration(optionNameUniswapValidTime),
			})
			if err != nil {
				return err
			}

			// Wait for termination or interrupt signals.
			// We want to clean up things at the end.
			interruptChannel := make(chan os.Signal, 1)
			signal.Notify(interruptChannel, syscall.SIGINT, syscall.SIGTERM)

			p := &program{
				start: func() {
					// Block main goroutine until it is interrupted
					sig := <-interruptChannel

					logger.Debugf("received signal: %v", sig)
					logger.Info("shutting down")
				},
				stop: func() {
					// Shutdown
					done := make(chan struct{})
					go func() {
						defer close(done)

						ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
						defer cancel()

						if err := a.Shutdown(ctx); err != nil {
							logger.Errorf("shutdown: %v", err)
						}
					}()

					// If shutdown function is blocking too long,
					// allow process termination by receiving another signal.
					select {
					case sig := <-interruptChannel:
						logger.Debugf("received signal: %v", sig)
					case <-done:
					}
				},
			}

			if isWindowsService {
				s, err := service.New(p, &service.Config{
					Name:        serviceName,
					DisplayName: "Ant",
					Description: "Ant, Sana client.",
				})
				if err != nil {
					return err
				}

				if err = s.Run(); err != nil {
					return err
				}
			} else {
				// start blocks until some interrupt is received
				p.start()
				p.stop()
			}

			return nil
		},
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return c.config.BindPFlags(cmd.Flags())
		},
	}

	c.setAllFlags(cmd)
	c.root.AddCommand(cmd)
	return nil
}

type program struct {
	start func()
	stop  func()
}

func (p *program) Start(s service.Service) error {
	// Start should not block. Do the actual work async.
	go p.start()
	return nil
}

func (p *program) Stop(s service.Service) error {
	p.stop()
	return nil
}

type signerConfig struct {
	signer           crypto.Signer
	publicKey        *ecdsa.PublicKey
	libp2pPrivateKey *ecdsa.PrivateKey
	pssPrivateKey    *ecdsa.PrivateKey
}

func waitForClef(logger logging.Logger, maxRetries uint64, endpoint string) (externalSigner *external.ExternalSigner, err error) {
	for {
		externalSigner, err = external.NewExternalSigner(endpoint)
		if err == nil {
			return externalSigner, nil
		}
		if maxRetries == 0 {
			return nil, err
		}
		maxRetries--
		logger.Warningf("failing to connect to clef signer: %v", err)

		time.Sleep(5 * time.Second)
	}
}

func (c *command) configureSigner(cmd *cobra.Command, logger logging.Logger) (config *signerConfig, err error) {
	var keystore keystore.Service
	if c.config.GetString(optionNameDataDir) == "" {
		keystore = memkeystore.New()
		logger.Warning("data directory not provided, keys are not persisted")
	} else {
		keystore = filekeystore.New(filepath.Join(c.config.GetString(optionNameDataDir), "keys"))
	}

	var signer crypto.Signer
	var password string
	var publicKey *ecdsa.PublicKey
	if p := c.config.GetString(optionNamePassword); p != "" {
		password = p
	} else if pf := c.config.GetString(optionNamePasswordFile); pf != "" {
		b, err := ioutil.ReadFile(pf)
		if err != nil {
			return nil, err
		}
		password = string(bytes.Trim(b, "\n"))
	} else {
		// if libp2p key exists we can assume all required keys exist
		// so prompt for a password to unlock them
		// otherwise prompt for new password with confirmation to create them
		exists, err := keystore.Exists("libp2p")
		if err != nil {
			return nil, err
		}
		if exists {
			password, err = terminalPromptPassword(cmd, c.passwordReader, "Password")
			if err != nil {
				return nil, err
			}
		} else {
			password, err = terminalPromptCreatePassword(cmd, c.passwordReader)
			if err != nil {
				return nil, err
			}
		}
	}

	if c.config.GetBool(optionNameClefSignerEnable) {
		endpoint := c.config.GetString(optionNameClefSignerEndpoint)
		if endpoint == "" {
			endpoint, err = clef.DefaultIpcPath()
			if err != nil {
				return nil, err
			}
		}

		externalSigner, err := waitForClef(logger, 5, endpoint)
		if err != nil {
			return nil, err
		}

		clefRPC, err := rpc.Dial(endpoint)
		if err != nil {
			return nil, err
		}

		wantedAddress := c.config.GetString(optionNameClefSignerEthereumAddress)
		var overlayEthAddress *common.Address = nil
		// if wantedAddress was specified use that, otherwise clef account 0 will be selected.
		if wantedAddress != "" {
			ethAddress := common.HexToAddress(wantedAddress)
			overlayEthAddress = &ethAddress
		}

		signer, err = clef.NewSigner(externalSigner, clefRPC, crypto.Recover, overlayEthAddress)
		if err != nil {
			return nil, err
		}

		publicKey, err = signer.PublicKey()
		if err != nil {
			return nil, err
		}
	} else {
		logger.Warning("clef is not enabled; portability and security of your keys is sub optimal")
		swarmPrivateKey, _, err := keystore.Key("sana", password)
		if err != nil {
			return nil, fmt.Errorf("sana key: %w", err)
		}
		signer = crypto.NewDefaultSigner(swarmPrivateKey)
		publicKey = &swarmPrivateKey.PublicKey
	}

	logger.Infof("sana public key %x", crypto.EncodeSecp256k1PublicKey(publicKey))

	libp2pPrivateKey, created, err := keystore.Key("libp2p", password)
	if err != nil {
		return nil, fmt.Errorf("libp2p key: %w", err)
	}
	if created {
		logger.Debugf("new libp2p key created")
	} else {
		logger.Debugf("using existing libp2p key")
	}

	pssPrivateKey, created, err := keystore.Key("pss", password)
	if err != nil {
		return nil, fmt.Errorf("pss key: %w", err)
	}
	if created {
		logger.Debugf("new pss key created")
	} else {
		logger.Debugf("using existing pss key")
	}

	logger.Infof("pss public key %x", crypto.EncodeSecp256k1PublicKey(&pssPrivateKey.PublicKey))

	// postinst and post scripts inside packaging/{deb,rpm} depend and parse on this log output
	overlayEthAddress, err := signer.EthereumAddress()
	if err != nil {
		return nil, err
	}
	logger.Infof("using ethereum address %x", overlayEthAddress)

	return &signerConfig{
		signer:           signer,
		publicKey:        publicKey,
		libp2pPrivateKey: libp2pPrivateKey,
		pssPrivateKey:    pssPrivateKey,
	}, nil
}

type networkConfig struct {
	bootNodes []string
	blockTime uint64
	chainID   int64
}

func getConfigByNetworkID(networkID uint64, defaultBlockTime uint64) *networkConfig {
	var config = networkConfig{
		blockTime: uint64(time.Duration(defaultBlockTime) * time.Second),
	}
	switch networkID {
	case 1:
		config.bootNodes = []string{"/dnsaddr/mainnet.ethsana.org"}
		config.chainID = 100

	case 5: //staging
		config.bootNodes = []string{"/dnsaddr/testnet.ethsana.org"}
		config.chainID = 5

	case 100:
		config.bootNodes = []string{"/dnsaddr/mainnet.ethsana.org"}
		config.chainID = 100

	case 31337:
		config.bootNodes = []string{}
		config.chainID = 31337
	default: //will use the value provided by the chain
		config.chainID = -1
	}

	return &config
}
