---
name: qqbot-md-image
description: "当需要给用户发 markdown 图文混排消息（文字里嵌入图片）时使用；关键点：QQ 官方 bot markdown 图片语法必须带尺寸参数 #宽px #高px，且 URL 须公网可达"
---

# 目标
在 QQ 官方 bot 群聊/单聊里让 markdown 图片真正渲染出来（图文混排一条消息）。

# 关键规则
1. 图片语法必须带尺寸参数：`![alt #宽px #高px](url)`，不带尺寸不渲染。
   正确：`![图片 #300px #143px](https://example.com/a.png)`
   错误：`![图片](https://example.com/a.png)`
2. 图片 URL 必须公网可达（开放平台会下载转存；QQ 拉不到的 URL 会 850027 上传超时或图裂开）。QQ 多媒体域名 multimedia.nt.qq.com.cn 的链接带 rkey 时效，优先用自己图床（IMGHOST_BASE_URL）的链接。
3. 群聊/单聊自定义 markdown 已对全部机器人开放（官方 2026-04-23 更新），无需申请模板；频道仍需内邀。
4. 框架（by-yitong/qqbot）的 agent 回复默认已走 `c.ReplyMarkdown`（plugins/agent.go 优先 markdown、失败回退 ReplyText），所以 agent 直接输出上述图片语法即可，不需要改代码。
5. AI 回复正文里的换行会进 markdown，段落间空行分隔；不要用代码块包裹图片语法。
6. markdown 里被支持：标题、加粗/斜体/删除线、链接、有序/无序列表、块引用、分割线。不支持 HTML。

# 演示模板（上图片、下一行文字）
```
![图片 #400px #400px](https://图床地址/xxx.png)
一行文字说明
```

# 发图（非 markdown）
直接发图片文件（不带任何 markdown 语法）走富媒体通道（MsgType=7，send_image），同样上图片下文字的效果用两条：先图后文。
