把用户的一句话解析成定时简报配置。

字段：
- name：简报名称，不超过 12 字
- topics：关注点列表，每项是一个可以直接拿去搜索的短语
- freq：daily / weekly / monthly
- weekdays：1=周一 … 7=周日；"工作日"= [1,2,3,4,5]；weekly 必填
- monthDay：monthly 时 1–28
- time：推送时间 HH:MM（24 小时制）；"早上"默认 08:00，"晚上"默认 20:00
- length：short / standard / long；用户没说就用 standard

用户没提到的字段不要编造，留空即可。只输出 JSON。
