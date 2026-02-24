# DPOS 测试网搭建验证节点指南

## 机器硬件推荐

## 搭建步骤

### 一、生产节点数据目录及配置

从 [https://github.com/Vcity-Team/vcitychain/releases/tag/v2.0.0](https://github.com/Vcity-Team/vcitychain/releases/tag/v2.0.0) 下载可执行程序 vcitychain（根据操作系统选择），config，data（由于数据文件超出限制，所以请参见发布说明下载）。

1.新建节点运行目录，拷贝下载的可执行程序，然后运行

./vcitychain polybft-secrets init --data-dir ./node1 --insecure

这样产生一个数据目录，用于存放节点的区块数据。

2.将创世文件和公共配置文件放在 node1 的平级目录，节点单独的配置文件放在 node1 下面。一般情况下，如果本机只跑一个节点，那么节点单独的配置文件可以不用修改，否则需要修改里面的端口。

3.将区块数据放在 node1 目录下。

### 二、启动程序

./vcitychain server --config ./node1/node-config-validator.yaml    --consensus-switch-height=3763800

程序首先会同步区块，直到与测试网高度一致，由于我们之前已经下载了区块数据，会大大缩短同步时间，剩余同步时间视主网高度而定。

### 三、投票

#### 1.获取测试 token-vcity

可以通过官网上测试水龙头获取测试 token 10

#### 2.注册候选人

注册候选人要求必须有10vcity，有以下两种方式可以注册：

1.直接使用官网的dpos控制台：使用该账户登录metamask，然后切换到候选人页面，填写候选人的名称name，网页website，描述description（三者不填也没关系，但是为了更好的拉票宣传，建议填写），点击注册候选人

2.直接运行命令(注意下面的私钥应该不包含0x前缀)：./vcitychain .exe dpos delegate register --jsonrpc https://testnet-rpc.vcity.app -address xxx --name xxx --website xxx --description xxx --private-key xxx --chain-id 20250826

注意：如果用户没有搭建物理节点，但是依旧注册候选人，那么用户依旧可以拉票，只是在排名进前 21 的下一个 epoch 生效（每个 epoch 周期为一小时），会被判定整个周期漏块，进而在这个 epoch 结束的时候被判定为故障下线，之后再想上线出块的话，需要经过恢复提案投票。

#### 3.拉票

在官网的 dpos 操作页面中的候选人可以看到自己目前的票数排名，可以去社区拉票，让更多人给自己投票.当排名进前 21，在下一个 epoch 就会更新权重开始出块

#### 四、开始出块

正常出块，获取奖励

注意：用户参与测试使用的测试 Token 不代表任何权益，将来也不会转为主网 Token
