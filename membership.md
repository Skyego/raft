Simon (@superfell) and I (@ongardie) talked through reworking this library's cluster membership changes last Friday. We don't see a way to split this into independent patches, so we're taking the next best approach: submitting the plan here for review, then working on an enormous PR. Your feedback would be appreciated. (@superfell is out this week, however, so don't expect him to respond quickly.)

主要更改
1.角色的新增和转换逻辑
2.配置文件的处理

主要目标：
1. 使事情符合博士论文中的描述；
2. catch up new servers 的优先级高于授予其投票权，同时允许永久无投票权节点存在；
(感觉就像是很多里面存在的那种最终一致性备份节点)
3. 去掉peers.json文件，避免log和snapshot之间的一致性问题。

## 数据为中心的视图
我们建议将*配置*重新定义为一组服务器，其中每台服务器都包括一个地址（就像今天一样）和一种模式，即：
1. *Voter*：投票会被统计，同时其index用于推进leader的commit index
2. *Nonvoter*：无投票权节点，接收日志条目但不考虑用于选举或承诺目的的服务器，即永久无投票权节点
3. *Staging*：即一开始是Nonvoter（无投票权节点），收到足够多的日志条目来足够赶上领导者的日志，领导者将调用成员资格变更，将 Staging 服务器更改为投票者。
   注意是 领导者主动执行的变更，谁负责检测合适符合要求了？ leader 吧

所有对于配置的变更都会通过向日志写入一个新的配置的方式实现。
新配置将会在append到日志里之后，立即生效（而不是像普通状态机命令一样提交时）[注：这里需要注意以及应该是不再通过放到状态机里进行执行了]。
根据论文一次最多可以有一个未提交的配置，在提交前一个配置之前，可能不会创建下一个配置。？？？ 是直接代码里不允许呢，还是可能不会可能会。
对于非服务器/登台服务器，严格来说没有必要遵循这些规则，但我们认为最好是统一对待所有更改。

每台服务器将跟踪两种配置：
1. *committed configuration*：日志/快照中已提交的最新配置及其索引（log index）。
2. *latest configuration*：日志/快照中的最新配置（可能已提交或未提交）及其索引。

如果没有成员变更，那么这两个配置相同。除以下情况外，大部分情况下，当前使用的配置信息是*latest configuration*：
1. 当follower 截断他们日志的后缀时，他们可能需要退回到提交的配置。[注：即这个配置已经无效了。]
2. 快照时，会写入提交的配置，以与正在快照的提交日志前缀相对应。
(the committed configuration is written, to correspond with the committed log prefix that is being snapshotted.)

## 应用程序接口
我们建议客户端执行以下操作来操纵集群配置：
- AddVoter:服务器变为暂存，除非投票者，
- AddNonvoter:添加nonvoter，服务器将成为nonvoter，除非暂存或投票者，
- DemoteVoter:降级Voter，除非缺席，否则服务器将成为nonvoter，
- RemovePeer:将服务器从配置中删除
- GetConfiguration：等待提交最新配置，返回提交的配置。
  虽然这些操作不是很对称，但我们认为它们是捕捉用户可能意图的良好设置。
  例如，如果我想确保某个服务器没有投票权，但该服务器根本不是配置的一部分，则可能不应将其添加为非投票服务器。

这些应用程序级OP中的每一项都将由leader进行解释，如果有影响（配置变化），将导致leader在其日志中写入新的配置条目。
导致 log entry写入的操作，不需要是日志项的一部分[注：命令日志写入的操作，不需要作为日志记录]。

# 代码实现
这是一个不完整的列表：
1. 移除PeerStore: 去掉了存储的peers.json文件的PeerStore(这个可能会导致和log/snapshot不同步，日志更改也很难原子性保存和维护，也不清楚它是要跟踪提交的配置还是最新的配置)。
2. 服务器必须搜索其快照和日志，才能在启动时找到提交的配置和最新的配置。
3. Bootstrap(引导程序)将不再使用peers.json，而是使用应用程序提供的configuration entry，来初始化日志或快照
4. 快照应存储cfg配置及其该配置对应的log index。根据我使用 LogCabin 的经验，配置的原始日志索引对于包含在调试日志消息中非常有用。
5. 配置变更请求应该通过一个单独的通道处理，并且只有等上一个committed之后，才能处理/创建新的，hashicorp/raft#84。
6. 对于日志caught up的判断，可以在单独的PR中实现。一个简单的方法是当Staging达到leader commit index 的95% 就提升为 Voter

These are the main goals:
 - Bringing things in line with the description in my PhD dissertation;
 - Catching up new servers prior to granting them a vote, as well as allowing permanent non-voting members; and
 - Eliminating the `peers.json` file, to avoid issues of consistency between that and the log/snapshot.

## Data-centric view

We propose to re-define a *configuration* as a set of servers, where each server includes an address (as it does today) and a mode that is either:
 - *Voter*: a server whose vote is counted in elections and whose match index is used in advancing the leader's commit index.
 - *Nonvoter*: a server that receives log entries but is not considered for elections or commitment purposes.
 - *Staging*: a server that acts like a nonvoter with one exception: once a staging server receives enough log entries to catch up sufficiently to the leader's log, the leader will invoke a  membership change to change the staging server to a voter.

All changes to the configuration will be done by writing a new configuration to the log. The new configuration will be in affect as soon as it is appended to the log (not when it is committed like a normal state machine command). Note that, per my dissertation, there can be at most one uncommitted configuration at a time (the next configuration may not be created until the prior one has been committed). It's not strictly necessary to follow these same rules for the nonvoter/staging servers, but we think its best to treat all changes uniformly.

Each server will track two configurations:
 1. its *committed configuration*: the latest configuration in the log/snapshot that has been committed, along with its index.
 2. its *latest configuration*: the latest configuration in the log/snapshot (may be committed or uncommitted), along with its index.

When there's no membership change happening, these two will be the same. The latest configuration is almost always the one used, except:
 - When followers truncate the suffix of their logs, they may need to fall back to the committed configuration.
 - When snapshotting, the committed configuration is written, to correspond with the committed log prefix that is being snapshotted.


## Application API

We propose the following operations for clients to manipulate the cluster configuration:
 - AddVoter: server becomes staging unless voter,
 - AddNonvoter: server becomes nonvoter unless staging or voter,
 - DemoteVoter: server becomes nonvoter unless absent,
 - RemovePeer: server removed from configuration,
 - GetConfiguration: waits for latest config to commit, returns committed config.

This diagram, of which I'm quite proud, shows the possible transitions:
```
+-----------------------------------------------------------------------------+
|                                                                             |
|                      Start ->  +--------+                                   |
|            ,------<------------|        |                                   |
|           /                    | absent |                                   |
|          /       RemovePeer--> |        | <---RemovePeer                    |
|         /            |         +--------+               \                   |
|        /             |            |                      \                  |
|   AddNonvoter        |         AddVoter                   \                 |
|       |       ,->---' `--<-.      |                        \                |
|       v      /              \     v                         \               |
|  +----------+                +----------+                    +----------+   |
|  |          | ---AddVoter--> |          | -log caught up --> |          |   |
|  | nonvoter |                | staging  |                    |  voter   |   |
|  |          | <-DemoteVoter- |          |                 ,- |          |   |
|  +----------+         \      +----------+                /   +----------+   |
|                        \                                /                   |
|                         `--------------<---------------'                    |
|                                                                             |
+-----------------------------------------------------------------------------+
```

While these operations aren't quite symmetric, we think they're a good set to capture
the possible intent of the user. For example, if I want to make sure a server doesn't have a vote, but the server isn't part of the configuration at all, it probably shouldn't be added as a nonvoting server.

Each of these application-level operations will be interpreted by the leader and, if it has an effect, will cause the leader to write a new configuration entry to its log. Which particular application-level operation caused the log entry to be written need not be part of the log entry.

## Code implications

This is a non-exhaustive list, but we came up with a few things:
- Remove the PeerStore: the `peers.json` file introduces the possibility of getting out of sync with the log and snapshot, and it's hard to maintain this atomically as the log changes. It's not clear whether it's meant to track the committed or latest configuration, either.
- Servers will have to search their snapshot and log to find the committed configuration and the latest configuration on startup.
- Bootstrap will no longer use `peers.json` but should initialize the log or snapshot with an application-provided configuration entry.
- Snapshots should store the index of their configuration along with the configuration itself. In my experience with LogCabin, the original log index of the configuration is very useful to include in debug log messages.
- As noted in hashicorp/raft#84, configuration change requests should come in via a separate channel, and one may not proceed until the last has been committed.
- As to deciding when a log is sufficiently caught up, implementing a sophisticated algorithm *is* something that can be done in a separate PR. An easy and decent placeholder is: once the staging server has reached 95% of the leader's commit index, promote it.

## Feedback

Again, we're looking for feedback here before we start working on this. Here are some questions to think about:
 - Does this seem like where we want things to go?
 - Is there anything here that should be left out?
 - Is there anything else we're forgetting about?
 - Is there a good way to break this up?
 - What do we need to worry about in terms of backwards compatibility?
 - What implication will this have on current tests?
 - What's the best way to test this code, in particular the small changes that will be sprinkled all over the library?
