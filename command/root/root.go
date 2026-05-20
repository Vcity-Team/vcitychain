package root

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command/backup"
	"github.com/Vcity-Team/vcitychain/command/bridge"
	"github.com/Vcity-Team/vcitychain/command/dpos"
	"github.com/Vcity-Team/vcitychain/command/genesis"
	"github.com/Vcity-Team/vcitychain/command/helper"
	"github.com/Vcity-Team/vcitychain/command/ibft"
	"github.com/Vcity-Team/vcitychain/command/license"
	"github.com/Vcity-Team/vcitychain/command/monitor"
	"github.com/Vcity-Team/vcitychain/command/peers"
	"github.com/Vcity-Team/vcitychain/command/polybft"
	"github.com/Vcity-Team/vcitychain/command/polybftsecrets"
	"github.com/Vcity-Team/vcitychain/command/rebuildcanonical"
	"github.com/Vcity-Team/vcitychain/command/regenesis"
	"github.com/Vcity-Team/vcitychain/command/rollback"
	"github.com/Vcity-Team/vcitychain/command/rootchain"
	"github.com/Vcity-Team/vcitychain/command/secrets"
	"github.com/Vcity-Team/vcitychain/command/server"
	"github.com/Vcity-Team/vcitychain/command/status"
	"github.com/Vcity-Team/vcitychain/command/txpool"
	"github.com/Vcity-Team/vcitychain/command/version"
)

type RootCommand struct {
	baseCmd *cobra.Command
}

func NewRootCommand() *RootCommand {
	rootCommand := &RootCommand{
		baseCmd: &cobra.Command{
			Short: "Polygon Edge is a framework for building Ethereum-compatible Blockchain networks",
		},
	}

	helper.RegisterJSONOutputFlag(rootCommand.baseCmd)

	rootCommand.registerSubCommands()

	return rootCommand
}

func (rc *RootCommand) registerSubCommands() {
	rc.baseCmd.AddCommand(
		version.GetCommand(),
		txpool.GetCommand(),
		status.GetCommand(),
		secrets.GetCommand(),
		peers.GetCommand(),
		rootchain.GetCommand(),
		monitor.GetCommand(),
		ibft.GetCommand(),
		dpos.GetCommand(),
		backup.GetCommand(),
		genesis.GetCommand(),
		server.GetCommand(),
		license.GetCommand(),
		polybftsecrets.GetCommand(),
		polybft.GetCommand(),
		bridge.GetCommand(),
		regenesis.GetCommand(),
		rollback.GetCommand(),
		rebuildcanonical.GetCommand(),
	)
}

func (rc *RootCommand) Execute() {
	if err := rc.baseCmd.Execute(); err != nil {
		// 快速重启时端口仍被占用，避免裸打 stderr（无 logger 前缀）刷屏
		if !isQuietStartupBindError(err) {
			_, _ = fmt.Fprintln(os.Stderr, err)
		}

		os.Exit(1)
	}
}

func isQuietStartupBindError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "resource temporarily unavailable")
}
