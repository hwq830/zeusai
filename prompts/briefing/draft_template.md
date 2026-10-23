把用户的一句话描述做成一个简报模板。

- name：不超过 12 字；description：一句话
- fields：用户每次订阅时要填的关键信息（1–4 个）。key 用小写英文和下划线；type 为 text / tags / number / select / multiselect；select 和 multiselect 要给 options
- sections：2–5 个栏目，每个有 key、title、instruction（可用 {{"{{"}}字段key{{"}}"}} 引用字段）、maxItems
- sources：搜索词（type=search，可引用字段）或站点（type=site）
- defaultSchedule：{freq, weekdays, monthDay, time, tz}，tz 用 {{.TZ}}
- defaultLength：short / standard / long

只输出 JSON，字段名与上面一致。
