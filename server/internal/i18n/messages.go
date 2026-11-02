package i18n

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// English texts of the user-facing error messages, keyed by the Chinese text the services
// return. A key may hold %s, %d or %q for a variable part; the English text uses %s for each,
// in the same order. The admin console is Chinese-only, so its messages are not listed.
// TestCatalogCoversErrors fails when a new user-facing message has no entry here.
var messagesEN = map[string]string{
	"开始日期不能晚于结束日期":                      "The start date must not be later than the end date",
	"日期格式不正确，请使用 YYYY-MM-DD":            "Invalid date. Use YYYY-MM-DD",
	"请求格式不正确":                           "The request is malformed",
	"参数不正确":                             "Invalid parameters",
	"该功能尚未开放":                           "This feature is not available yet",
	"内容未通过安全审核，请修改后重试":                  "This content didn't pass our safety review. Please edit it and try again",
	"服务器开小差了，请稍后再试":                     "Something went wrong on our side. Please try again later",
	"请先登录":                              "Please sign in first",
	"登录已过期":                             "Your session has expired",
	"登录已过期，请重新登录":                       "Your session has expired. Please sign in again",
	"账号不存在或已注销":                         "This account doesn't exist or has been deleted",
	"账号已被停用，如有疑问请联系客服":                  "This account has been suspended. Contact support if you have questions",
	"手机号格式不正确":                          "Invalid phone number",
	"验证码发送太频繁，请稍后再试":                    "Too many code requests. Please try again later",
	"短信发送失败，请稍后再试":                      "Couldn't send the text message. Please try again later",
	"验证码错误或已过期":                         "The code is wrong or has expired",
	"登录服务暂时不可用，请稍后再试":                   "Sign-in is temporarily unavailable. Please try again later",
	"该登录方式暂未开放，请使用其他方式登录":               "This sign-in method isn't available yet. Please use another one",
	"内容审核服务暂时不可用，请稍后再试":                 "Content review is temporarily unavailable. Please try again later",
	"模型服务繁忙，请稍后再试":                      "The AI service is busy. Please try again later",
	"时区无效":                              "Invalid time zone",
	"不存在":                               "Not found",
	"只支持英文译文（en）":                       "Only English translations (en) are supported",
	"英文名称不超过 60 个字符，简介不超过 300 个字符":      "English names can be up to 60 characters and descriptions up to 300",
	"%s不存在":                             "Not found",
	"decision 应为 approve / reject":      "decision must be approve or reject",
	"上传失败，请重试":                          "Upload failed. Please try again",
	"发送失败，请稍后再试":                        "Couldn't send. Please try again later",
	"提交失败，请稍后再试":                        "Couldn't submit. Please try again later",
	"内容不能为空":                            "Content can't be empty",
	"内容不能超过 1000 字":                     "Content can't be longer than 1,000 characters",
	"同时进行的任务太多，请等当前任务完成":                "Too many tasks are running. Please wait for the current ones to finish",
	"任务不存在":                             "Task not found",
	"任务已结束":                             "The task has already finished",
	"任务启动失败，请稍后再试":                      "Couldn't start the task. Please try again later",
	"中止失败，请稍后再试":                        "Couldn't stop the task. Please try again later",
	"这个确认已失效":                           "This confirmation has expired",
	"研究报告功能暂时不可用，请稍后再试":                 "Research reports are temporarily unavailable. Please try again later",
	"调研题目不能为空且不超过 500 字":                "The research topic must be 1–500 characters",
	"depth 应为 quick / standard / deep":  "depth must be quick, standard or deep",
	"侧重角度最多 6 个":                        "Pick at most 6 focus angles",
	"报告不存在":                             "Report not found",
	"报告还没有生成完":                          "The report isn't finished yet",
	"报告完成后才能评价":                         "You can rate the report once it's finished",
	"报告生成完后才能分享":                        "You can share the report once it's finished",
	"简报还没有生成完":                          "The briefing isn't finished yet",
	"简报生成完后才能分享":                        "You can share the briefing once it's finished",
	"分享功能暂时不可用，请稍后再试":                   "Sharing is temporarily unavailable. Please try again later",
	"有效期应为 0（永久）到 365 天":                "The expiry must be 0 (never) to 365 days",
	"今天分享次数太多了，明天再试吧":                   "You've shared a lot today. Please try again tomorrow",
	"这份内容暂时不能公开分享":                      "This content can't be shared publicly",
	"这一条含个人信息，不能分享":                     "This item contains personal information and can't be shared",
	"这份简报都是个人内容，不能分享":                   "This briefing only contains personal content and can't be shared",
	"单条分享需要 itemId":                     "itemId is required to share one item",
	"scope 应为 full / summary / item":    "scope must be full, summary or item",
	"分享已过期":                             "This link has expired",
	"这一条不存在":                            "This item doesn't exist",
	"报告生成完后才能追问":                        "You can ask follow-up questions once the report is finished",
	"问题不能为空且不超过 1000 字":                 "The question must be 1–1,000 characters",
	"rating 应为 satisfied / unsatisfied": "rating must be satisfied or unsatisfied",
	"rating 应为 useful / meh":            "rating must be useful or meh",
	"未知的原因：%s":                          "Unknown reason: %s",
	"补充说明不能超过 500 字":                    "The comment can't be longer than 500 characters",
	"scope 应为 full / summary":           "scope must be full or summary",
	"分享不存在":                             "Share not found",
	"分享已失效":                             "This link is no longer available",
	"PDF 导出即将开放":                        "PDF export is coming soon",
	"PDF 导出暂时不可用，请稍后再试":                 "PDF export is temporarily unavailable. Please try again later",
	"简报不存在":                             "Briefing not found",
	"简报名称不能为空且不超过 40 字":                 "The briefing name must be 1–40 characters",
	"关注点最多 10 个":                        "Add at most 10 topics",
	"请至少填写一个关注点或选择模板":                   "Add at least one topic or pick a template",
	"请填写「%s」":                           "Please fill in “%s”",
	"请描述你想要的简报":                         "Describe the briefing you want",
	"没能理解这句话，换个说法试试":                    "Couldn't understand that. Try saying it another way",
	"创建定时任务失败，请稍后再试":                    "Couldn't create the schedule. Please try again later",
	"更新定时任务失败，请稍后再试":                    "Couldn't update the schedule. Please try again later",
	"删除定时任务失败，请稍后再试":                    "Couldn't delete the schedule. Please try again later",
	"最多同时保留 %d 个定时简报":                   "You can keep at most %s scheduled briefings",
	"freq 应为 daily / weekly / monthly":  "freq must be daily, weekly or monthly",
	"时间格式应为 HH:MM":                      "Time must be HH:MM",
	"weekdays 取值为 1–7":                  "weekdays must be 1–7",
	"每周简报需要选择星期":                        "Pick the days for a weekly briefing",
	"每月简报的日期为 1–28":                     "The day of a monthly briefing must be 1–28",
	"模板不存在":                             "Template not found",
	"模板不存在或不可修改":                        "Template not found or can't be edited",
	"模板不存在或不可删除":                        "Template not found or can't be deleted",
	"模板名称不能为空且不超过 40 字":                 "The template name must be 1–40 characters",
	"模板需要 1–8 个栏目":                      "A template needs 1–8 sections",
	"字段最多 12 个":                         "A template can have at most 12 fields",
	"字段 key %q 无效或重复":                   "Field key %s is invalid or used twice",
	"地区 %q 无效或重复":                       "Region %s is invalid or listed twice",
	"地区 %q 无效":                          "Invalid region %s",
	"每个地区的信息来源最多 20 条":                  "At most 20 sources per region",
	"%s的信息来源无效":                         "Invalid sources for %s",
	"地区无效":                              "Invalid region",
	"字段类型 %q 无效":                        "Field type %s is invalid",
	"字段 %q 需要选项":                        "Field %s needs options",
	"模板内容地区无效":                          "Invalid template content region",
	"栏目内容地区无效":                          "Invalid section content region",
	"栏目标题不能为空":                          "Section titles can't be empty",
	"信息来源无效":                            "Invalid source",
	"篇幅应为 short / standard / long":      "Length must be short, standard or long",
	"自定义模板最多 20 个":                      "You can have at most 20 custom templates",
	"请描述你想要的简报模板":                       "Describe the briefing template you want",
	"没能生成模板，换个说法试试":                     "Couldn't create a template. Try saying it another way",
	"没能生成可用的模板，换个说法试试":                  "Couldn't create a usable template. Try saying it another way",
	"连接器不存在":                            "Connector not found",
	"该连接器的授权即将开放":                       "Connecting this service is coming soon",
	"请先授权「%s」":                          "Please connect “%s” first",
	"记忆不存在":                             "Memory not found",
	"消息不存在":                             "Message not found",
	"消息类别不正确":                           "Invalid message category",
	"免打扰时间格式应为 HH:MM":                   "Quiet hours must be HH:MM",
	"免打扰开始和结束时间不能相同":                    "Quiet hours can't start and end at the same time",
	"反馈不存在":                             "Feedback not found",
	"请选择反馈类型":                           "Pick a feedback type",
	"请再多描述一点（至少 5 个字）":                  "Please tell us a bit more (at least 5 characters)",
	"联系方式太长":                            "The contact info is too long",
	"最多 3 张截图":                          "At most 3 screenshots",
	"截图已失效，请重新上传":                       "The screenshot has expired. Please upload it again",
	"暂时无法上传截图":                          "Screenshots can't be uploaded right now",
	"只支持 JPEG 图片":                       "Only JPEG images are supported",
	"图片不能超过 5 MB":                       "Images can't be larger than 5 MB",
	"今天提交的反馈太多了，明天再来吧":                  "You've sent a lot of feedback today. Please try again tomorrow",
	"本月 token 额度已用完，%s重置；定时简报照常推送":      "You've used this month's tokens. They reset on %s; scheduled briefings keep coming",
	"本月剩余 token 不够生成一份报告，%s重置；定时简报照常推送": "Not enough tokens left this month for a report. They reset on %s; scheduled briefings keep coming",
	"本月 token 额度已用完，%s重置":               "You've used this month's tokens. They reset on %s",
}

// Generic English messages by error code, for anything without a catalog entry.
var codeEN = map[string]string{
	"unauthenticated":  "Please sign in again",
	"forbidden":        "You don't have access to this",
	"not_found":        "Not found",
	"invalid_argument": "Some of the information isn't valid",
	"conflict":         "This was changed elsewhere. Please refresh and try again",
	"rate_limited":     "Too many requests. Please try again later",
	"unavailable":      "This is temporarily unavailable. Please try again later",
	"quota_exceeded":   "You've reached your limit",
	"content_blocked":  "This content didn't pass our safety review. Please edit it and try again",
	"internal":         "Something went wrong on our side. Please try again later",
}

type pattern struct {
	re *regexp.Regexp
	en string
}

var exactEN, patternsEN = func() (map[string]string, []pattern) {
	exact := map[string]string{}
	var pats []pattern
	verb := regexp.MustCompile(`%[sdq]`)
	for zh, en := range messagesEN {
		if !verb.MatchString(zh) {
			exact[zh] = en
			continue
		}
		parts := verb.Split(zh, -1)
		for i, p := range parts {
			parts[i] = regexp.QuoteMeta(p)
		}
		pats = append(pats, pattern{regexp.MustCompile("^" + strings.Join(parts, "(.+?)") + "$"), en})
	}
	return exact, pats
}()

// Message translates a user-facing error message. Chinese passes through (converted for
// Traditional Chinese); English falls back to the error code's generic text.
func Message(l Lang, code, zh string) string {
	if l != EN {
		return l.Zh(zh)
	}
	if en, ok := exactEN[zh]; ok {
		return en
	}
	for _, p := range patternsEN {
		if m := p.re.FindStringSubmatch(zh); m != nil {
			args := make([]any, len(m)-1)
			for i, a := range m[1:] {
				args[i] = englishArg(strings.Trim(a, `"`))
			}
			return sprintf(p.en, args)
		}
	}
	if en, ok := codeEN[code]; ok {
		return en
	}
	return codeEN["internal"]
}

var cnDate = regexp.MustCompile(`^(\d{1,2}) 月 (\d{1,2}) 日$`)

// englishArg rewrites the Chinese dates services put into messages ("11 月 1 日" → "November 1").
func englishArg(a string) string {
	if m := cnDate.FindStringSubmatch(a); m != nil {
		mo, _ := strconv.Atoi(m[1])
		if mo >= 1 && mo <= 12 {
			return time.Month(mo).String() + " " + m[2]
		}
	}
	return a
}

func sprintf(format string, args []any) string {
	var b strings.Builder
	for i := 0; ; i++ {
		before, after, ok := strings.Cut(format, "%s")
		b.WriteString(before)
		if !ok {
			return b.String()
		}
		if i < len(args) {
			b.WriteString(args[i].(string))
		}
		format = after
	}
}
