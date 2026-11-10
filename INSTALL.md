# راهنمای نصب Learning Hub

Learning Hub یک فایل اجرایی تنهاست (`hub.exe` برای ویندوز، `hub` برای لینوکس). قالب‌ها، مهارت‌ها و مهاجرت‌های پایگاه داده درون همین فایل‌اند و به نصب چیز دیگری جز ابزار AI نیاز ندارد.

## پیش‌نیاز: ابزار AI

دست‌کم **یکی** از سه ابزار زیر باید روی سیستم نصب، در `PATH` و وارد حساب باشد. Learning Hub خودش چیزی نصب یا وارد نمی‌کند؛ فقط ابزار را در حالت بدون رابط (headless) اجرا می‌کند. `hub doctor` ابزارِ `[adapter] kind` را می‌سنجد (نسخه، ورود، پشتیبانی از `stream-json` و `--json-schema`).

### Antigravity (`agy`) — پیش‌فرض

1. `agy` را نصب کنید و `agy --version` اجرا شود (آزموده با 1.5.4).
2. یک‌بار `agy` را باز کنید و `/login` بزنید. `agy models` باید فهرست مدل‌ها را چاپ کند؛ در غیر این صورت کارها با «نیاز به ورود» متوقف می‌شوند.
3. مدل پیش‌فرض `gemini-3.8-flash-medium` است؛ اگر در `agy models` نیست، `[adapter] model` را عوض کنید.
4. برنامه `agy` را این‌گونه اجرا می‌کند:

   ```
   agy --dangerously-skip-permissions -p <prompt> --output-format stream-json --json-schema <work>/output.schema.json --model <model> --effort <effort> --print-timeout <t> --sandbox
   ```

   - `--dangerously-skip-permissions` با `[adapter] skip_permissions` (پیش‌فرض `true`) می‌آید؛ بدون آن `agy` در حالت headless هر درخواست مجوز (مثلاً `read_file`) را رد می‌کند و نوبت **بدون خروجی** تمام می‌شود.
   - این پرچم فقط همراه `--sandbox` مجاز است: `skip_permissions = true` با `sandbox = false` ⇐ برنامه اجرا نمی‌شود. گذاشتن این پرچم در `extra_args` هم رد می‌شود.
   - **ریسک:** با این پرچم مدل می‌تواند بیرون از پوشه کار هم بخواند/بنویسد. دایرکتوری پایه را داخل مخزن کد یا پوشه حساس نگذارید. اگر نمی‌خواهید، `skip_permissions = false` بگذارید (احتمال خروجی خالی بالا می‌رود).

### Claude Code (`claude`)

1. Claude Code را نصب کنید و `claude --version` اجرا شود؛ نسخه باید `--json-schema` و `--output-format stream-json` را در `claude --help` داشته باشد.
2. یک‌بار `claude` را باز کنید و وارد شوید؛ `claude auth status` باید `loggedIn: true` بدهد.
3. برنامه آن را با `-p … --tools Read,Glob,Grep --allowedTools Read,Glob,Grep --no-session-persistence --strict-mcp-config --setting-sources project` اجرا می‌کند: فقط خواندن، بدون MCP، hook و پلاگین‌های کاربر. نیازی به تنظیم مجوز یا `--dangerously-skip-permissions` نیست (و در `extra_args` ممنوع است).

### Codex (`codex`)

1. Codex CLI را نصب کنید و `codex --version` اجرا شود؛ `codex exec --help` باید `--json` داشته باشد.
2. `codex login` را اجرا کنید؛ `codex login status` باید موفق باشد.
3. برنامه آن را با `codex exec --json --skip-git-repo-check --sandbox read-only …` اجرا می‌کند؛ پرچم‌های `--sandbox`، `--yolo`، `--full-auto` و `--dangerously-bypass-approvals-and-sandbox` در `extra_args` ممنوع‌اند.

## نصب

1. یک پوشه برای داده‌های یادگیری بسازید؛ مثلاً `C:\Users\<شما>\LearningHub`. این «دایرکتوری پایه» است و همه درس‌ها، تمرین‌ها، پاسخ‌ها و گزارش‌ها در آن ذخیره می‌شوند. آن را **داخل پوشه همگام‌شده ابری** (OneDrive، Dropbox) نگذارید.
2. `hub.exe` را در همین پوشه یا هر جای دیگر کپی کنید.
3. در ترمینال، داخل دایرکتوری پایه، سلامت محیط را بسنجید:

   ```
   hub.exe doctor
   ```

   خروجی باید با `[OK] ready` تمام شود. در غیر این صورت پیام آن مشخص می‌کند چه چیزی کم است.

## اجرا

داخل دایرکتوری پایه:

```
hub.exe serve
```

سپس مرورگر را روی <http://127.0.0.1:8787> باز کنید. در اولین اجرا صفحه تنظیمات باز می‌شود؛ پس از خواندن متن حریم خصوصی (محتوا و پاسخ‌ها برای ابزار AI فرستاده می‌شوند) آن را تأیید کنید.

- بدون `--config`، پوشه جاری دایرکتوری پایه است و تنظیمات پیش‌فرض به کار می‌رود.
- با `hub.exe serve --config <مسیر>\config.toml` می‌توانید `base_dir`، مدل (`[adapter] model`) و ویرایشگر (`[editor] command`) را تعیین کنید. اگر Jobها با «ابزار AI مدتی پاسخ نداد» شکست خوردند، `[jobs] stall_timeout` (پیش‌فرض `"6m"`) را افزایش دهید. مسیر پیش‌فرض فایل تنظیمات: `%APPDATA%\LearningHub\config.toml` (ویندوز) یا `~/.config/learninghub/config.toml`.
- **انتخاب ابزار AI:** در صفحه «تنظیمات» بین `antigravity` (agy)، `claude` (Claude Code) و `codex` یکی را انتخاب کنید؛ انتخاب در پایگاه داده ذخیره می‌شود و فقط وقتی هیچ کاری در صف یا در حال اجرا نیست قابل تغییر است. ابزار پیش‌فرض قبل از انتخاب: `[adapter] kind`. هر ابزار باید طبق بخش «پیش‌نیاز» نصب و وارد شده باشد. تنظیمات `agy` کلیدهای بالای `[adapter]` است؛ مدل و مسیر Claude و Codex جداگانه تنظیم می‌شود (خالی = پیش‌فرض خود ابزار):

  ```toml
  [adapter]
  kind             = "antigravity"   # antigravity | claude | codex
  binary           = "agy"
  model            = "gemini-3.8-flash-medium"
  effort           = "medium"        # low | medium | high | xhigh | max
  sandbox          = true
  skip_permissions = true            # فقط agy؛ نیازمند sandbox = true
  extra_args       = []

  [adapter.claude]
  binary = "claude"
  model  = "sonnet"
  effort = "medium"

  [adapter.codex]
  binary = "codex"
  model  = ""
  ```
- **مهارت‌ها:** اولین اجرا `AGENTS.md`، `profile.md` و مهارت‌ها را در `.agent/<نام>/SKILL.md` دایرکتوری پایه می‌سازد. وقتی نسخه جدیدتری از یک مهارت درون برنامه باشد، صفحه تنظیمات «نسخه جدید مهارت» را نشان می‌دهد؛ ارتقا فقط با دکمه انجام می‌شود و از نسخه قبلی پشتیبان (`SKILL.<نسخه>.bak.md`) می‌ماند.
- **ورود Roadmap:** پس از آپلود، تشخیص سرفصل‌ها با AI خودکار اجرا می‌شود و پیشنهاد درختی (هر سرفصل یک رکورد، والد/فرزند) اعمال می‌شود. تأیید تا رسیدن پیشنهاد غیرفعال است؛ اگر AI در دسترس نبود، «ادامه بدون AI» همه سرفصل‌ها را رکورد می‌کند.
- سرور فقط روی `127.0.0.1` گوش می‌دهد و از شبکه بیرونی در دسترس نیست. در هر لحظه فقط یک نمونه اجرا می‌شود.

## فرمان‌ها

| فرمان | کار |
|---|---|
| `hub serve` | اجرای برنامه (پیش‌فرض) |
| `hub doctor [--json]` | بررسی ابزار AI و نوشتنی‌بودن دایرکتوری پایه |
| `hub migrate` | ساخت/به‌روزرسانی پایگاه داده `app.db` |
| `hub rebuild [--db path]` | بازسازی پایگاه داده از روی فایل‌ها روی DB خالی (فایل‌ها فقط خوانده می‌شوند) |
| `hub version` | نسخه |

## پشتیبان و بازیابی

- برنامه روزانه نسخه پشتیبان `app.db` را در `.app/backup/` می‌سازد (۷ نسخه آخر).
- فایل‌های زیر `topics/` و `roadmaps/` منبع اصلی‌اند؛ با از دست رفتن `app.db` می‌توانید با `hub rebuild` آن را دوباره بسازید.

## ساخت از سورس

```
make release   # آزمون‌ها، سپس dist/hub.exe و dist/hub
make vuln      # govulncheck (به دسترسی به vuln.go.dev نیاز دارد)
```

## چک‌لیست بررسی دستی روی ویندوز

پیش از استفاده روزمره، این موارد را یک‌بار روی ویندوز واقعی بررسی کنید:

- [ ] `hub.exe doctor` با `[OK] ready` تمام می‌شود.
- [ ] `hub.exe serve` اجرا و صفحه در مرورگر باز می‌شود؛ اجرای نمونه دوم با پیام «Another instance» رد می‌شود.
- [ ] ورود یک Roadmap، تولید درس، تولید تمرین و ارزیابی یک پاسخ با `agy` واقعی موفق است (و با `claude`/`codex` اگر از آن‌ها استفاده می‌کنید).
- [ ] لاگ Job در `.app/logs/jobs/<id>/` نشان می‌دهد argv با `--dangerously-skip-permissions` شروع می‌شود و `denied_actions` خالی است.
- [ ] لغو یک کار در حال اجرا، پردازش `agy` و فرزندانش را در Task Manager می‌بندد.
- [ ] بستن ناگهانی پنجره `hub.exe` هیچ پردازش `agy` یتیمی باقی نمی‌گذارد؛ اجرای دوباره کار نیمه‌تمام را «قطع‌شده» نشان می‌دهد.
- [ ] دکمه «باز کردن» فایل پاسخ را در ویرایشگر تنظیم‌شده باز می‌کند.
- [ ] مسیر طولانی (دایرکتوری پایه عمیق) و نام فارسی موضوع مشکلی ایجاد نمی‌کند.
- [ ] باز بودن فایل پاسخ در ویرایشگر هنگام ارزیابی باعث خطا یا بازنویسی نمی‌شود.
