package verify

import (
	"crypto/rand"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Logiase/MiraiGo-Template/bot"
	"github.com/Logiase/MiraiGo-Template/utils"
	"github.com/Mrs4s/MiraiGo/client"
	"github.com/Mrs4s/MiraiGo/message"
)

var instance *logging
var logger = utils.GetModuleLogger("com.aimerneige.verify")
var maxTryTimes = 5

// TODO race condition / dead lock
var pendingList map[int64][]pendingData

type logging struct {
}

type pendingData struct {
	Group  int64
	Times  int
	Verify string
}

func init() {
	instance = &logging{}
	bot.RegisterModule(instance)
	pendingList = make(map[int64][]pendingData, 5)
}

func (l *logging) MiraiGoModule() bot.ModuleInfo {
	return bot.ModuleInfo{
		ID:       "com.aimerneige.verify",
		Instance: instance,
	}
}

// Init 初始化过程
// 在此处可以进行 Module 的初始化配置
// 如配置读取
func (l *logging) Init() {
}

// PostInit 第二次初始化
// 再次过程中可以进行跨 Module 的动作
// 如通用数据库等等
func (l *logging) PostInit() {
}

// Serve 注册服务函数部分
func (l *logging) Serve(b *bot.Bot) {
	b.GroupMemberJoinEvent.Subscribe(func(c *client.QQClient, event *client.MemberJoinGroupEvent) {
		// 没有管理员权限的群，忽略
		if !event.Group.AdministratorOrOwner() {
			return
		}
		group := event.Group
		member := event.Member
		verifyCode := generateVerifyCode(6)
		// 插入检查队列
		insertPending(member.Uin, group.Code, verifyCode)
		go func() {
			// 等五分钟
			time.Sleep(time.Minute * 5)
			// 检查是否还在队列中
			if _, ok := pendingList[member.Uin]; ok {
				for _, data := range pendingList[member.Uin] {
					if data.Group == group.Code {
						// 验证超时，移出群
						if !member.Manageable() {
							clearVerify(member.Uin, group.Code)
							errMsg := fmt.Sprintf("无法移除群成员「%d」，请检查是否授予机器人管理员权限。", member.Uin)
							c.SendGroupMessage(group.Code, simpleText(errMsg))
							return
						}
						if err := member.Kick("验证超时", false); err != nil {
							errMsg := fmt.Sprintf("在将成员「%d」移出群「%d」的过程中发生错误，详情请查阅后台日志。", member.Uin, group.Code)
							logger.WithError(err).Errorf(errMsg)
							c.SendGroupMessage(group.Code, simpleText(errMsg))
							return
						}
						clearVerify(member.Uin, group.Code)
						kickMsg := fmt.Sprintf("用户「%d」超时未通过认证，已被移出群聊。", member.Uin)
						c.SendGroupMessage(group.Code, simpleText(kickMsg))
						return
					}
				}
			}
		}()
		welcomeMsg := fmt.Sprintf("欢迎来到群「%s」\n请在五分钟内发送验证码【%s】\n超时或错误会被移出群聊，请认真输入。", group.Name, verifyCode)
		c.SendGroupMessage(group.Code, withAt(member.Uin, welcomeMsg))
	})
	b.GroupMessageEvent.Subscribe(func(c *client.QQClient, msg *message.GroupMessage) {
		// 用户在等待队列，处理其消息
		if _, ok := pendingList[msg.Sender.Uin]; ok {
			ok, times := checkVerify(msg.Sender.Uin, msg.GroupCode, msg.ToString())
			// 没找到，不是这个群，忽略
			if times == -1 {
				return
			}
			// 验证通过，发送欢迎消息
			if ok {
				welcomeMsg := fmt.Sprintf("恭喜用户「%d」通过了入群验证，快来和大家打个招呼吧！", msg.Sender.Uin)
				c.SendGroupMessage(msg.GroupCode, withAt(msg.Sender.Uin, welcomeMsg))
				return
			}
			// 超过规定次数，验证失败，踢出
			if times >= maxTryTimes {
				senderInfo, err := c.GetMemberInfo(msg.GroupCode, msg.Sender.Uin)
				if err != nil {
					errMsg := fmt.Sprintf("在群「%d」获取成员「%d」的用户数据时发成错误，详情请查阅后台日志。", msg.GroupCode, msg.Sender.Uin)
					logger.WithError(err).Errorf(errMsg)
					c.SendGroupMessage(msg.GroupCode, simpleText(errMsg))
					return
				}
				if !senderInfo.Manageable() {
					clearVerify(msg.Sender.Uin, msg.GroupCode)
					errMsg := fmt.Sprintf("无法移除群成员「%d」，请检查是否授予机器人管理员权限。", msg.Sender.Uin)
					c.SendGroupMessage(msg.GroupCode, simpleText(errMsg))
					return
				}
				if err := senderInfo.Kick("验证失败", false); err != nil {
					errMsg := fmt.Sprintf("在将成员「%d」移出群「%d」的过程中发生错误，详情请查阅后台日志。", msg.Sender.Uin, msg.GroupCode)
					logger.WithError(err).Errorf(errMsg)
					c.SendGroupMessage(msg.GroupCode, simpleText(errMsg))
					return
				}
				clearVerify(msg.Sender.Uin, msg.GroupCode)
				kickMsg := fmt.Sprintf("用户「%d」已尝试「%d」次均未通过认证，已被移出群聊。", msg.Sender.Uin, times)
				c.SendGroupMessage(msg.GroupCode, simpleText(kickMsg))
				return
			}
			notifyMsg := fmt.Sprintf("验证失败！您还有「%d」次验证机会。", maxTryTimes-times)
			c.SendGroupMessage(msg.GroupCode, withAt(msg.Sender.Uin, notifyMsg))
		}
	})
}

// Start 此函数会新开携程进行调用
// ```go
//
//	go exampleModule.Start()
//
// ```
// 可以利用此部分进行后台操作
// 如 http 服务器等等
func (l *logging) Start(b *bot.Bot) {
}

// Stop 结束部分
// 一般调用此函数时，程序接收到 os.Interrupt 信号
// 即将退出
// 在此处应该释放相应的资源或者对状态进行保存
func (l *logging) Stop(b *bot.Bot, wg *sync.WaitGroup) {
	// 别忘了解锁
	defer wg.Done()
}

func simpleText(msg string) *message.SendingMessage {
	return message.NewSendingMessage().Append(message.NewText(msg))
}

func withAt(target int64, msg string) *message.SendingMessage {
	return message.NewSendingMessage().Append(message.NewAt(target)).Append(message.NewText(msg))
}

// generateVerifyCode 生成随机验证码
func generateVerifyCode(max int) string {
	var table = [...]byte{'1', '2', '3', '4', '5', '6', '7', '8', '9', '0'}
	b := make([]byte, max)
	n, err := io.ReadAtLeast(rand.Reader, b, max)
	if n != max {
		panic(err)
	}
	for i := 0; i < len(b); i++ {
		b[i] = table[int(b[i])%len(table)]
	}
	return string(b)
}

// insertPending 插入等待队列
func insertPending(uin int64, groupCode int64, verify string) {
	pendingList[uin] = append(pendingList[uin], pendingData{
		Group:  groupCode,
		Times:  0,
		Verify: verify,
	})
}

// checkVerify 检查验证码
func checkVerify(uin int64, groupCode int64, verify string) (bool, int) {
	// 没找到用户，返回 -1
	if _, ok := pendingList[uin]; !ok {
		return false, -1
	}
	for i, data := range pendingList[uin] {
		if data.Group == groupCode {
			// 找到了，校验
			if data.Verify == verify {
				// 校验通过，移除队列
				clearVerify(uin, groupCode)
				return true, 0
			}
			// 验证失败，增加校验次数
			pendingList[uin][i].Times++
			return false, pendingList[uin][i].Times
		}
	}
	// 没找到群，返回 -1
	return false, -1
}

// clearVerify 删除验证信息
func clearVerify(uin int64, groupCode int64) {
	if _, ok := pendingList[uin]; ok {
		for i, data := range pendingList[uin] {
			if data.Group == groupCode {
				// 删除用户队列下指定群的验证信息
				pendingList[uin] = append(pendingList[uin][:i], pendingList[uin][i+1:]...)
				// 如果全部的群验证信息都被删除，移除用户队列
				if len(pendingList[uin]) == 0 {
					delete(pendingList, uin)
				}
			}
		}
	}
}
