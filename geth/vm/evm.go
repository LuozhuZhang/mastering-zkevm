// location: geth/core/vm/evm.go

// Copyright 2014 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package vm

import (
	"math/big"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// * create使用emptyCodeHash，为了防止部署到已经部署了合约的地址
// * 有意思的是，这玩意跟account abstraction有关系，后面可以研究一下（放这里有点像一个todo）
// emptyCodeHash is used by create to ensure deployment is disallowed to already
// deployed contract addresses (relevant after the account abstraction).
var emptyCodeHash = crypto.Keccak256Hash(nil)

// * 各种transfer的签名函数，以及blockhash
type (
	// CanTransferFunc is the signature of a transfer guard function
	// * transfer guard function（还不知道这是个啥东西）的签名函数
	CanTransferFunc func(StateDB, common.Address, *big.Int) bool
	// TransferFunc is the signature of a transfer function
	// * transfer function的签名函数
	TransferFunc func(StateDB, common.Address, common.Address, *big.Int)
	// GetHashFunc returns the n'th block hash in the blockchain
	// and is used by the BLOCKHASH EVM op code.
	// * 貌似使用BLOCKHASH这个opcode，并且会返回对应block的hash
	GetHashFunc func(uint64) common.Hash
)

// * precompile -> 预编译，可能是为了节省时间，看看具体是怎么work的（这里也是作为一个工具函数）
func (evm *EVM) precompile(addr common.Address) (PrecompiledContract, bool) {
	var precompiles map[common.Address]PrecompiledContract
	switch {
	case evm.chainRules.IsBerlin:
		precompiles = PrecompiledContractsBerlin
	case evm.chainRules.IsIstanbul:
		precompiles = PrecompiledContractsIstanbul
	case evm.chainRules.IsByzantium:
		precompiles = PrecompiledContractsByzantium
	default:
		precompiles = PrecompiledContractsHomestead
	}
	p, ok := precompiles[addr]
	return p, ok
}

// * BlockContext给EVM提供一些相关信息（在zkevm的witness中也有类似的信息） -> 而且类似于常量，提供以后就不会改变
// BlockContext provides the EVM with auxiliary information. Once provided
// it shouldn't be modified.
type BlockContext struct {
	// CanTransfer returns whether the account contains
	// sufficient ether to transfer the value
	// * CanTransferFunc返回一个bool
	// * 表示一个账户是否有足够的ether来转账
	CanTransfer CanTransferFunc
	// Transfer transfers ether from one account to the other
	// * TransferFunc是一个签名函数，用来在A -> B的时候签名
	Transfer TransferFunc
	// GetHash returns the hash corresponding to n
	// * 拿到对应block的一个hash
	GetHash GetHashFunc

	// Block information
	// * 一些常量信息，我们要多关注gas相关的信息
	// * 其中GasLimit、BaseFee、Random并不是很了解
	Coinbase    common.Address // Provides information for COINBASE
	GasLimit    uint64         // Provides information for GASLIMIT
	BlockNumber *big.Int       // Provides information for NUMBER
	Time        *big.Int       // Provides information for TIME
	Difficulty  *big.Int       // Provides information for DIFFICULTY
	BaseFee     *big.Int       // Provides information for BASEFEE
	Random      *common.Hash   // Provides information for RANDOM
}

// * TxContext给EVM提供一些tx相关的信息 -> 根据tx的变化，这些信息貌似也可以改变
// TxContext provides the EVM with information about a transaction.
// All fields can change between transactions.
type TxContext struct {
	// Message information
	Origin common.Address // Provides information for ORIGIN
	// * 所以gasPrice是每笔transaction的？
	// * gasLimit是一个block可用的？
	GasPrice *big.Int // Provides information for GASPRICE
}

// EVM is the Ethereum Virtual Machine base object and provides
// the necessary tools to run a contract on the given state with
// the provided context.（1） It should be noted that any error
// generated through any of the calls should be considered a
// revert-state-and-consume-all-gas operation, no checks on
// specific errors should ever be performed. The interpreter makes
// sure that any errors generated are to be considered faulty code.

// * 1.EVM通过一个给定的状态（given state）和给定的上下文（provided context）来运行contract的代码
// * 所以上面的BlockContext和TxContext都是为了让EVM运行contract而给定的context

// * 2.任何调用call而生成的error，都应该被视作“revert-state-and-consume-all-gas”操作 ->
// * revert state(恢复状态) & consume all gas(消耗gas)，所以理论上应该是要上链的，为什么我们之前的error不上链呢？

// ![issue] * 3.interpreter会保证所有errors都被视为faulty code（这是个什么东西）

// ![issue] * 4.EVM不能被复用，而且不是thread safe的 -> 这里也没搞懂什么意思

// The EVM should never be reused and is not thread safe.
// * 这里应该就是EVM需要的所有context
type EVM struct {
	// Context provides auxiliary blockchain related information
	Context BlockContext
	TxContext
	// StateDB gives access to the underlying state
	// * 这里应该是就是为了拿到依赖的state，上面是跑EVM必须的context（block和tx）
	StateDB StateDB
	// Depth is the current call stack
	// ![issue] 不太清楚这里的depth -> current call stack是什么东西，不过既然是EVM相关的信息，还是有必要搞懂的
	depth int

	// chainConfig contains information about the current chain
	// * 当前chain的信息，Genesis block也需要导入这个（比如chainId什么的）
	// * 这里面定义了许多字段，貌似有一些历史渊源，不太清楚为什么
	chainConfig *params.ChainConfig
	// chain rules contains the chain rules for the current epoch
	// * 选择不同的epoch，可能是不同的fork版本
	chainRules params.Rules
	// virtual machine configuration options used to initialise the
	// evm.
	// * 这里是Interpreter里面的config，用来初始化EVM
	// * Debug、Tracer、NoBaseFee(EIP1559)、EnablePreimageRecording等（设置启动还是关闭）
	Config Config
	// global (to this context) ethereum virtual machine
	// used throughout the execution of the tx.
	// * EVM的interpreter，可以理解为把solidity -> byte/pc/stack/op等东西，让EVM能够理解并执行
	// ![issue] 非常重要
	interpreter *EVMInterpreter
	// abort is used to abort the EVM calling operations
	// NOTE: must be set atomically
	// * abort用来终止call operation
	// ![issue] 而且被设置为atomically -> 不清楚什么意思
	abort int32
	// callGasTemp holds the gas available for the current call. This is needed because the
	// available gas is calculated in gasCall* according to the 63/64 rule and later
	// applied in opCall*.
	// * callGasTemp保存当前call的gas available（可用的gas，或者一个call消耗的gas）
	// ![issue] 63/64的byte规则，以及opCall*这个东西
	callGasTemp uint64
}

// NewEVM returns a new EVM. The returned EVM is not thread safe and should
// only ever be used *once*.
// * 用来返回一个EVM实例，而且这个实例只能跑一次（也是thread unsafe的）
func NewEVM(blockCtx BlockContext, txCtx TxContext, statedb StateDB, chainConfig *params.ChainConfig, config Config) *EVM {
	evm := &EVM{
		Context:     blockCtx,
		TxContext:   txCtx,
		StateDB:     statedb,
		Config:      config,
		chainConfig: chainConfig,
		chainRules:  chainConfig.Rules(blockCtx.BlockNumber, blockCtx.Random != nil),
	}
	// * 而且也创建一个新的EVM interpreter -> 根据每个block吗，还是根据每个transaction（甚至每个call）
	// * 应该是给外部（执行contract代码的地方调用的）
	evm.interpreter = NewEVMInterpreter(evm, config)
	return evm
}

// Reset resets the EVM with a new transaction context.Reset
// This is not threadsafe and should only be done very cautiously.
// * 重置EVM -> 传入新的TxContext和StateDB
func (evm *EVM) Reset(txCtx TxContext, statedb StateDB) {
	evm.TxContext = txCtx
	evm.StateDB = statedb
}

// Cancel cancels any running EVM operation. This may be called concurrently and
// it's safe to be called multiple times.、
// * 取消EVM的操作
func (evm *EVM) Cancel() {
	atomic.StoreInt32(&evm.abort, 1)
}

// Cancelled returns true if Cancel has been called
// * 检查是否调用了Cancel（取消EVM operation的函数）
func (evm *EVM) Cancelled() bool {
	return atomic.LoadInt32(&evm.abort) == 1
}

// * 返回current interpreter
// Interpreter returns the current interpreter
func (evm *EVM) Interpreter() *EVMInterpreter {
	return evm.interpreter
}

// Call executes the contract associated with the addr with the given input as
// parameters. It also handles any necessary value transfer required and takes
// the necessary steps to create accounts and reverses the state in case of an
// execution error or failed value transfer.

// * 1.使用input data作为参数，执行某个contract addr相关的代码

// * 2.这个函数可以执行必要的value transfer，以及create accounts

// * 3.如果出现了execution error，或者failed value transfer，可以revert state（需要消耗gas吗？这个怎么上链）
func (evm *EVM) Call(caller ContractRef, addr common.Address, input []byte, gas uint64, value *big.Int) (ret []byte, leftOverGas uint64, err error) {
	// Fail if we're trying to execute above the call depth limit
	// * 需要搞懂depth是什么（应该是stack的深度）
	// * 如果我们这笔交易需要的depth(evm.depth)超过了params.CallCreateDepth（call/create最大的depth -> 1024）就会报错
	if evm.depth > int(params.CallCreateDepth) {
		return nil, gas, ErrDepth
	}
	// Fail if we're trying to transfer more than the available balance
	// * 如果call时候transfer value超过balance，就会报错 -> 就是我们导出error trace的地方
	// * 我感觉这里应该是caller的balance -> 所以我们的出发点就错了，应该是调用合约账户的余额不足，而不是合约账户的余额不足
	if value.Sign() != 0 && !evm.Context.CanTransfer(evm.StateDB, caller.Address(), value) {
		// ! Error的位置
		return nil, gas, ErrInsufficientBalance
	}
	// * 如果通过了上面两个error check，就可以更新snapshot（也就是state）
	snapshot := evm.StateDB.Snapshot()
	// * 还不太清楚precompile主要做什么，传入contract address，返回不同的版本的compile？
	// ![issue] precompile是什么？
	p, isPrecompile := evm.precompile(addr)

	if !evm.StateDB.Exist(addr) {
		if !isPrecompile && evm.chainRules.IsEIP158 && value.Sign() == 0 {
			// * 如果调用了一个不存在的合约地址（!evm.stateDB.Exist）
			// Calling a non existing account, don't do anything, but ping the tracer
			if evm.Config.Debug {
				if evm.depth == 0 {
					// * 使用debug tracer -> 不太清楚要做什么
					// ![issue] tracer是什么？
					evm.Config.Tracer.CaptureStart(evm, caller.Address(), addr, false, input, gas, value)
					evm.Config.Tracer.CaptureEnd(ret, 0, 0, nil)
				} else {
					evm.Config.Tracer.CaptureEnter(CALL, caller.Address(), addr, input, gas, value)
					evm.Config.Tracer.CaptureExit(ret, 0, nil)
				}
			}
			return nil, gas, nil
		}
		// * 如果调用的地址不存在，EVM会创建这个地址
		evm.StateDB.CreateAccount(addr)
	}
	// * 创建之后再transfer/call一下
	evm.Context.Transfer(evm.StateDB, caller.Address(), addr, value)

	// Capture the tracer start/end events in debug mode
	// * tracer相关的东西，跟上面一样，我不太清楚tracer是什么 -> 参考geth.doc，貌似就是debug.trace的参数，有很多tracer的模式
	if evm.Config.Debug {
		if evm.depth == 0 {
			evm.Config.Tracer.CaptureStart(evm, caller.Address(), addr, false, input, gas, value)
			defer func(startGas uint64, startTime time.Time) { // Lazy evaluation of the parameters
				evm.Config.Tracer.CaptureEnd(ret, startGas-gas, time.Since(startTime), err)
			}(gas, time.Now())
		} else {
			// Handle tracer events for entering and exiting a call frame
			evm.Config.Tracer.CaptureEnter(CALL, caller.Address(), addr, input, gas, value)
			defer func(startGas uint64) {
				evm.Config.Tracer.CaptureExit(ret, startGas-gas, err)
			}(gas)
		}
	}

	if isPrecompile {
		ret, gas, err = RunPrecompiledContract(p, input, gas)
	} else {
		// Initialise a new contract and set the code that is to be used by the EVM.
		// The contract is a scoped environment for this execution context only.
		// * 获取合约地址对应的code
		code := evm.StateDB.GetCode(addr)
		if len(code) == 0 {
			ret, err = nil, nil // gas is unchanged
		} else {
			addrCopy := addr
			// If the account has no code, we can abort here
			// The depth-check is already done, and precompiles handled above
			contract := NewContract(caller, AccountRef(addrCopy), value, gas)
			// * 调用这个合约
			contract.SetCallCode(&addrCopy, evm.StateDB.GetCodeHash(addrCopy), code)
			ret, err = evm.interpreter.Run(contract, input, false)
			gas = contract.Gas
		}
	}
	// When an error was returned by the EVM or when setting the creation code
	// above we revert to the snapshot and consume any gas remaining. Additionally
	// when we're in homestead this also counts for code storage gas errors.

	// * 1.当某个错误发生的时候，我们revert snapshot，并消耗掉剩余的gas（consume any gas remaining）

	// ![issue] 2.homestead是什么？-> 当我们在homestead时，这个也算作code storage gas error

	// * 3.所以我们实际执行ErrDepth检查（通过）、ErrInsufficientBalance检查（弹出错误），最后就是这个RevertToSnapshot

	if err != nil {
		// * 如果有error，就把snapshot的状态给revert
		evm.StateDB.RevertToSnapshot(snapshot)
		if err != ErrExecutionReverted {
			gas = 0
		}
		// TODO: consider clearing up unused snapshots:
		//} else {
		//	evm.StateDB.DiscardSnapshot(snapshot)
	}
	return ret, gas, err
}

// CallCode executes the contract associated with the addr with the given input
// as parameters. It also handles any necessary value transfer required and takes
// the necessary steps to create accounts and reverses the state in case of an
// execution error or failed value transfer.
//
// CallCode differs from Call in the sense that it executes the given address'
// code with the caller as context.
func (evm *EVM) CallCode(caller ContractRef, addr common.Address, input []byte, gas uint64, value *big.Int) (ret []byte, leftOverGas uint64, err error) {
	// Fail if we're trying to execute above the call depth limit
	if evm.depth > int(params.CallCreateDepth) {
		return nil, gas, ErrDepth
	}
	// Fail if we're trying to transfer more than the available balance
	// Note although it's noop to transfer X ether to caller itself. But
	// if caller doesn't have enough balance, it would be an error to allow
	// over-charging itself. So the check here is necessary.
	if !evm.Context.CanTransfer(evm.StateDB, caller.Address(), value) {
		return nil, gas, ErrInsufficientBalance
	}
	var snapshot = evm.StateDB.Snapshot()

	// Invoke tracer hooks that signal entering/exiting a call frame
	if evm.Config.Debug {
		evm.Config.Tracer.CaptureEnter(CALLCODE, caller.Address(), addr, input, gas, value)
		defer func(startGas uint64) {
			evm.Config.Tracer.CaptureExit(ret, startGas-gas, err)
		}(gas)
	}

	// It is allowed to call precompiles, even via delegatecall
	if p, isPrecompile := evm.precompile(addr); isPrecompile {
		ret, gas, err = RunPrecompiledContract(p, input, gas)
	} else {
		addrCopy := addr
		// Initialise a new contract and set the code that is to be used by the EVM.
		// The contract is a scoped environment for this execution context only.
		contract := NewContract(caller, AccountRef(caller.Address()), value, gas)
		contract.SetCallCode(&addrCopy, evm.StateDB.GetCodeHash(addrCopy), evm.StateDB.GetCode(addrCopy))
		ret, err = evm.interpreter.Run(contract, input, false)
		gas = contract.Gas
	}
	if err != nil {
		evm.StateDB.RevertToSnapshot(snapshot)
		if err != ErrExecutionReverted {
			gas = 0
		}
	}
	return ret, gas, err
}

// DelegateCall executes the contract associated with the addr with the given input
// as parameters. It reverses the state in case of an execution error.
//
// DelegateCall differs from CallCode in the sense that it executes the given address'
// code with the caller as context and the caller is set to the caller of the caller.
func (evm *EVM) DelegateCall(caller ContractRef, addr common.Address, input []byte, gas uint64) (ret []byte, leftOverGas uint64, err error) {
	// Fail if we're trying to execute above the call depth limit
	if evm.depth > int(params.CallCreateDepth) {
		return nil, gas, ErrDepth
	}
	var snapshot = evm.StateDB.Snapshot()

	// Invoke tracer hooks that signal entering/exiting a call frame
	if evm.Config.Debug {
		evm.Config.Tracer.CaptureEnter(DELEGATECALL, caller.Address(), addr, input, gas, nil)
		defer func(startGas uint64) {
			evm.Config.Tracer.CaptureExit(ret, startGas-gas, err)
		}(gas)
	}

	// It is allowed to call precompiles, even via delegatecall
	if p, isPrecompile := evm.precompile(addr); isPrecompile {
		ret, gas, err = RunPrecompiledContract(p, input, gas)
	} else {
		addrCopy := addr
		// Initialise a new contract and make initialise the delegate values
		contract := NewContract(caller, AccountRef(caller.Address()), nil, gas).AsDelegate()
		contract.SetCallCode(&addrCopy, evm.StateDB.GetCodeHash(addrCopy), evm.StateDB.GetCode(addrCopy))
		ret, err = evm.interpreter.Run(contract, input, false)
		gas = contract.Gas
	}
	if err != nil {
		evm.StateDB.RevertToSnapshot(snapshot)
		if err != ErrExecutionReverted {
			gas = 0
		}
	}
	return ret, gas, err
}

// StaticCall executes the contract associated with the addr with the given input
// as parameters while disallowing any modifications to the state during the call.
// Opcodes that attempt to perform such modifications will result in exceptions
// instead of performing the modifications.
func (evm *EVM) StaticCall(caller ContractRef, addr common.Address, input []byte, gas uint64) (ret []byte, leftOverGas uint64, err error) {
	// Fail if we're trying to execute above the call depth limit
	if evm.depth > int(params.CallCreateDepth) {
		return nil, gas, ErrDepth
	}
	// We take a snapshot here. This is a bit counter-intuitive, and could probably be skipped.
	// However, even a staticcall is considered a 'touch'. On mainnet, static calls were introduced
	// after all empty accounts were deleted, so this is not required. However, if we omit this,
	// then certain tests start failing; stRevertTest/RevertPrecompiledTouchExactOOG.json.
	// We could change this, but for now it's left for legacy reasons
	var snapshot = evm.StateDB.Snapshot()

	// We do an AddBalance of zero here, just in order to trigger a touch.
	// This doesn't matter on Mainnet, where all empties are gone at the time of Byzantium,
	// but is the correct thing to do and matters on other networks, in tests, and potential
	// future scenarios
	evm.StateDB.AddBalance(addr, big0)

	// Invoke tracer hooks that signal entering/exiting a call frame
	if evm.Config.Debug {
		evm.Config.Tracer.CaptureEnter(STATICCALL, caller.Address(), addr, input, gas, nil)
		defer func(startGas uint64) {
			evm.Config.Tracer.CaptureExit(ret, startGas-gas, err)
		}(gas)
	}

	if p, isPrecompile := evm.precompile(addr); isPrecompile {
		ret, gas, err = RunPrecompiledContract(p, input, gas)
	} else {
		// At this point, we use a copy of address. If we don't, the go compiler will
		// leak the 'contract' to the outer scope, and make allocation for 'contract'
		// even if the actual execution ends on RunPrecompiled above.
		addrCopy := addr
		// Initialise a new contract and set the code that is to be used by the EVM.
		// The contract is a scoped environment for this execution context only.
		contract := NewContract(caller, AccountRef(addrCopy), new(big.Int), gas)
		contract.SetCallCode(&addrCopy, evm.StateDB.GetCodeHash(addrCopy), evm.StateDB.GetCode(addrCopy))
		// When an error was returned by the EVM or when setting the creation code
		// above we revert to the snapshot and consume any gas remaining. Additionally
		// when we're in Homestead this also counts for code storage gas errors.
		ret, err = evm.interpreter.Run(contract, input, true)
		gas = contract.Gas
	}
	if err != nil {
		evm.StateDB.RevertToSnapshot(snapshot)
		if err != ErrExecutionReverted {
			gas = 0
		}
	}
	return ret, gas, err
}

type codeAndHash struct {
	code []byte
	hash common.Hash
}

func (c *codeAndHash) Hash() common.Hash {
	if c.hash == (common.Hash{}) {
		c.hash = crypto.Keccak256Hash(c.code)
	}
	return c.hash
}

// create creates a new contract using code as deployment code.
// * 这个函数可以创建一个合约，并且把代码给部署上去 -> Workflow
// * 1.通过三个preCheck：depth、balance、nonce
// * 2.把address添加到access-list
// * 3.检查部署地址有没有code，没有的话就创建一个合约账户
// * 	 初始化一个地址，并且部署codehash
// * 4.调用interpreter.Run来运行合约代码，检查代码size是否超过上限
// * 5.计算存储代码的gas是否足够
// * 6.出现任何交易就revert snapshot
// *   注意ErrCodeStoreOutOfGas不会上链，其他错误消耗gas并上链

func (evm *EVM) create(caller ContractRef, codeAndHash *codeAndHash, gas uint64, value *big.Int, address common.Address, typ OpCode) ([]byte, common.Address, uint64, error) {
	// Depth check execution. Fail if we're trying to execute above the
	// limit.
	// * 要通过以下几个preCheck
	// * 1）检查depth
	// * 2）检查caller的balance是否充足 -> 所以这里的caller是contract？
	// * 		不过这里要通过balance的检查，说明caller余额是够的
	// * 3）nonce是正确的
	if evm.depth > int(params.CallCreateDepth) {
		return nil, common.Address{}, gas, ErrDepth
	}
	if !evm.Context.CanTransfer(evm.StateDB, caller.Address(), value) {
		return nil, common.Address{}, gas, ErrInsufficientBalance
	}
	nonce := evm.StateDB.GetNonce(caller.Address())
	if nonce+1 < nonce {
		return nil, common.Address{}, gas, ErrNonceUintOverflow
	}
	evm.StateDB.SetNonce(caller.Address(), nonce+1)
	// We add this to the access list _before_ taking a snapshot. Even if the creation fails,
	// the access-list change should not be rolled back
	// * 把这个地址添加到access-list（在snapshot之前）
	// * 而且就算creation失败了，这个access-list也不会回滚
	if evm.chainRules.IsBerlin {
		evm.StateDB.AddAddressToAccessList(address)
	}
	// Ensure there's no existing contract already at the designated address
	// * 检查这个合约地址之前是否部署过代码，通过之前的那个emptyCodeHash工具
	// * 如果这个地址已经有代码，弹出ErrContractAddressCollision
	contractHash := evm.StateDB.GetCodeHash(address)
	if evm.StateDB.GetNonce(address) != 0 || (contractHash != (common.Hash{}) && contractHash != emptyCodeHash) {
		return nil, common.Address{}, 0, ErrContractAddressCollision
	}
	// * 如果通过了emptyCodeHash检查，就可以创建一个新的合约账户了
	// Create a new account on the state
	snapshot := evm.StateDB.Snapshot()
	evm.StateDB.CreateAccount(address) // * 创建新合约账户 -> 也说明报错在创建了新合约之后
	if evm.chainRules.IsEIP158 {
		evm.StateDB.SetNonce(address, 1)
	}
	evm.Context.Transfer(evm.StateDB, caller.Address(), address, value)

	// Initialise a new contract and set the code that is to be used by the EVM.
	// The contract is a scoped environment for this execution context only.
	// * 初始化一个新的合约，并且部署EVM code
	// ![issue] 这里说contract只是一个execution context，是什么意思？是因为sandbox model吗
	contract := NewContract(caller, AccountRef(address), value, gas)
	contract.SetCodeOptionalHash(&address, codeAndHash)

	// * 正常操作，开启tracer和debug
	if evm.Config.Debug {
		if evm.depth == 0 {
			evm.Config.Tracer.CaptureStart(evm, caller.Address(), address, true, codeAndHash.code, gas, value)
		} else {
			evm.Config.Tracer.CaptureEnter(typ, caller.Address(), address, codeAndHash.code, gas, value)
		}
	}

	start := time.Now()

	// * 调用interpreter.Run来运行合约代码 -> Run里面也有一个OOG error，要注意这一点
	// ![issue] 还不确定Run里面的OOG会不会有影响
	ret, err := evm.interpreter.Run(contract, nil, false)

	// Check whether the max code size has been exceeded, assign err if the case.
	// * 检查代码的size是否超过了最大上限，超过了就弹出 ErrMaxCodeSizeExceeded
	if err == nil && evm.chainRules.IsEIP158 && len(ret) > params.MaxCodeSize {
		err = ErrMaxCodeSizeExceeded
	}

	// Reject code starting with 0xEF if EIP-3541 is enabled.
	// * 如果启动了EIP3541（不知道这是什么），就会拒绝0xEF开头的代码
	if err == nil && len(ret) >= 1 && ret[0] == 0xEF && evm.chainRules.IsLondon {
		err = ErrInvalidCode
	}

	// if the contract creation ran successfully and no errors were returned
	// calculate the gas required to store the code. If the code could not
	// be stored due to not enough gas set an error and let it be handled
	// by the error checking condition below.

	// * 重要的位置
	// * 1）如果合约creation运行顺利（唯一不清楚的只有Run），并且没有弹出error，到这里就开始检查存储代码需要消耗的Gas是否充足
	// * 2）所以触发这个错误 -> 合约创建/合约代码不会有问题（要再看一下run函数是干嘛的），只是存储合约代码的gas不够

	if err == nil {
		// ! 要研究一下部署代码时候的gas怎么计算
		// * 在这里我直观感觉应该是一个byte消耗200的gas，所以bytes长度 * 200
		// * len(ret)应该是bytecode的长度
		// * CreateDataGas是常数，设置成了200 -> 最后算出的createDataGas应该就是存储合约代码需要消耗的gas
		createDataGas := uint64(len(ret)) * params.CreateDataGas
		// * UseGas函数检查是否有足够的gas
		// ![issue] 问题来了，是否有足够的gas指的是谁？-> c.Gas -> 这里的gas很有可能是部署合约的时候传进去的参数
		if contract.UseGas(createDataGas) {
			// * 如果gas足够则部署
			evm.StateDB.SetCode(address, ret)
		} else {
			// ! Error的位置
			// * gas不够就弹出我们需要的OOG error
			err = ErrCodeStoreOutOfGas
		}
	}

	// When an error was returned by the EVM or when setting the creation code
	// above we revert to the snapshot and consume any gas remaining. Additionally
	// when we're in homestead this also counts for code storage gas errors.
	// ! Error的位置
	// * 如果产生了error就revert snapshot，并且消费任意的一笔gas
	// * 跟Call函数的最后一步差不多
	if err != nil && (evm.chainRules.IsHomestead || err != ErrCodeStoreOutOfGas) {
		evm.StateDB.RevertToSnapshot(snapshot)
		// * OK 所以每次发生ErrExecutionReverted的时候交易都不上链
		// * 如果不是这个error，就会消费gas，并且失败的交易被提交到链上
		if err != ErrExecutionReverted {
			contract.UseGas(contract.Gas)
		}
	}

	if evm.Config.Debug {
		if evm.depth == 0 {
			evm.Config.Tracer.CaptureEnd(ret, gas-contract.Gas, time.Since(start), err)
		} else {
			evm.Config.Tracer.CaptureExit(ret, gas-contract.Gas, err)
		}
	}
	return ret, address, contract.Gas, err
}

// Create creates a new contract using code as deployment code.
func (evm *EVM) Create(caller ContractRef, code []byte, gas uint64, value *big.Int) (ret []byte, contractAddr common.Address, leftOverGas uint64, err error) {
	contractAddr = crypto.CreateAddress(caller.Address(), evm.StateDB.GetNonce(caller.Address()))
	return evm.create(caller, &codeAndHash{code: code}, gas, value, contractAddr, CREATE)
}

// Create2 creates a new contract using code as deployment code.
//
// The different between Create2 with Create is Create2 uses keccak256(0xff ++ msg.sender ++ salt ++ keccak256(init_code))[12:]
// instead of the usual sender-and-nonce-hash as the address where the contract is initialized at.
func (evm *EVM) Create2(caller ContractRef, code []byte, gas uint64, endowment *big.Int, salt *uint256.Int) (ret []byte, contractAddr common.Address, leftOverGas uint64, err error) {
	codeAndHash := &codeAndHash{code: code}
	contractAddr = crypto.CreateAddress2(caller.Address(), salt.Bytes32(), codeAndHash.Hash().Bytes())
	return evm.create(caller, codeAndHash, gas, endowment, contractAddr, CREATE2)
}

// ChainConfig returns the environment's chain configuration
func (evm *EVM) ChainConfig() *params.ChainConfig { return evm.chainConfig }
