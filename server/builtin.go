package server

import (
	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/consensus"
	consensusDev "github.com/Vcity-Team/vcitychain/consensus/dev"
	consensusDummy "github.com/Vcity-Team/vcitychain/consensus/dummy"
	consensusIBFT "github.com/Vcity-Team/vcitychain/consensus/ibft"
	consensusPolyBFT "github.com/Vcity-Team/vcitychain/consensus/polybft"
	"github.com/Vcity-Team/vcitychain/forkmanager"
	"github.com/Vcity-Team/vcitychain/secrets"
	"github.com/Vcity-Team/vcitychain/secrets/awsssm"
	"github.com/Vcity-Team/vcitychain/secrets/gcpssm"
	"github.com/Vcity-Team/vcitychain/secrets/hashicorpvault"
	"github.com/Vcity-Team/vcitychain/secrets/local"
	"github.com/Vcity-Team/vcitychain/state"
)

type GenesisFactoryHook func(config *chain.Chain, engineName string) func(*state.Transition) error

type ConsensusType string

type ForkManagerFactory func(forks *chain.Forks) error

type ForkManagerInitialParamsFactory func(config *chain.Chain) (*forkmanager.ForkParams, error)

const (
	DevConsensus     ConsensusType = "dev"
	IBFTConsensus    ConsensusType = "ibft"
	PolyBFTConsensus ConsensusType = consensusPolyBFT.ConsensusName
	DummyConsensus   ConsensusType = "dummy"
)

var consensusBackends = map[ConsensusType]consensus.Factory{
	DevConsensus:     consensusDev.Factory,
	IBFTConsensus:    consensusIBFT.Factory,
	PolyBFTConsensus: consensusPolyBFT.Factory,
	DummyConsensus:   consensusDummy.Factory,
}

// secretsManagerBackends defines the SecretManager factories for different
// secret management solutions
var secretsManagerBackends = map[secrets.SecretsManagerType]secrets.SecretsManagerFactory{
	secrets.Local:          local.SecretsManagerFactory,
	secrets.HashicorpVault: hashicorpvault.SecretsManagerFactory,
	secrets.AWSSSM:         awsssm.SecretsManagerFactory,
	secrets.GCPSSM:         gcpssm.SecretsManagerFactory,
}

var genesisCreationFactory = map[ConsensusType]GenesisFactoryHook{
	PolyBFTConsensus: consensusPolyBFT.GenesisPostHookFactory,
}

var forkManagerFactory = map[ConsensusType]ForkManagerFactory{
	PolyBFTConsensus: consensusPolyBFT.ForkManagerFactory,
}

var forkManagerInitialParamsFactory = map[ConsensusType]ForkManagerInitialParamsFactory{
	PolyBFTConsensus: consensusPolyBFT.ForkManagerInitialParamsFactory,
}

func ConsensusSupported(value string) bool {
	_, ok := consensusBackends[ConsensusType(value)]

	return ok
}
