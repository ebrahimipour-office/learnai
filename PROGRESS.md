# وضعیت پیشرفت و ردیاب پیاده‌سازی پروژه (Learning Hub Progress Tracker)

این سند «حافظه پیوسته» و قطب‌نمای پروژه است. هر بار که چت جدیدی با هوش مصنوعی باز شود، هوش مصنوعی با خواندن این فایل دقیقاً می‌داند در چه مرحله‌ای هستیم، چه کارهایی انجام شده و بلافاصله کار بعدی چیست.

---

## 📍 وضعیت جاری پروژه (Current State Pointer)

| مشخصه | وضعیت |
|---|---|
| **فاز فعال کنونی** | **فاز ۳ — هسته Job Runner و تولید درس (Phase 3)** — فاز ۲ کامل شد ✅ |
| **کار بعدی برای اجرا** | **Task T3.8 — پنل موضوع و کارت پیشرفت زنده Job با htmx** |
| **آخرین کامیت Git** | `feat(render): T3.7 safe markdown rendering with RTL/LTR` |
| **⚠️ باز از T3.1** | «یک اجرای واقعی دستی موفق» هنوز انجام نشده: `agy` 1.4.12 (۲۰۲۶-۱۱-۰۱) همچنان `Eligibility check failed: ... not currently available in your location` در سطح حساب. پاکت خطای واقعی: `{"event":"result","result":{conversation_id,status,response,error,duration_seconds,num_turns,usage{input/output/thinking/cache_read/total_tokens},finished_reason}}`. **پاکت موفق دیده نشده:** نام فیلد خروجی ساختاریافته (`structured_output` با fallback به JSON داخل `response`)، رویدادهای میانی (`init`/`step_update`) و قالب `agy models` فرض‌اند — پس از اولین اجرای واقعی بررسی شوند (`internal/ai/antigravity/stream.go`). |
| **وضعیت آزمون‌های خودکار** | **۱۰۰٪ سبز و موفق** (`go test ./...` روی همه پکیج‌ها) |
| **مسیر دایرکتوری پایه** | `C:\Users\Asus\Desktop\Leaning-appication\prj` |

---

## 🤖 دستورالعمل هوش مصنوعی در شروع هر چت جدید (AI Session Protocol)

هوش مصنوعی در هر چت جدید که کاربر بگوید «ادامه بده»، «continue»، یا در مورد وضعیت پروژه بپرسد، **موظف است این ۵ گام را به ترتیب طی کند**:

1. **بررسی این فایل (`PROGRESS.md`):** کار بعدی مشخص‌شده در بخش «وضعیت جاری» را بیابد.
2. **سنجش سلامت کد:** دستور `go test ./...` را اجرا کند تا مطمئن شود محیط هنوز سبز و کامپایل‌پذیر است.
3. **اعلام وضعیت به کاربر:** با یک پیام کوتاه و شفاف به کاربر بگوید در کدام فاز و کدام تسک هستیم و اجرای آن را آغاز کند.
4. **پیاده‌سازی تسک جاری:** با رویکرد TDD، کد و آزمون‌های واحد آن تسک را بنویسد و تست کند.
5. **به‌روزرسانی این فایل و کامیت Git:** پس از تکمیل تسک، جدول زیر و کامیت Git را به‌روزرسانی کند و نشانگر «کار بعدی» را به تسک بعد منتقل کند.

---

## 📋 جدول ردیابی وضعیت ۴۵ کار اجرایی (Task Tracking Matrix)

### فاز ۱ — آماده‌سازی و زیرساخت (تکمیل‌شده ✅)
- [x] **T1.1**: بررسی امکان‌سنجی `agy` و فرمان `hub doctor` (پایش موفق نسخه Go `1.26.4` و `agy 1.1.12`)
- [x] **T1.2**: الگوی اصلی `skills/AGENTS.md.tmpl` با اصول هشت‌گانه رفتار مدل
- [x] **T1.3**: الگوی پروفایل یادگیرنده در `skills/profile.md.tmpl`
- [x] **T1.4**: مهارت‌های سه‌گانه پایه در `skills/` (`lesson-generator`, `exercise-generator`, `answer-evaluator`)
- [x] **T1.5**: اسکلت CLI (`cmd/hub/main.go`) با زیرفرمان‌های `version`, `doctor`, `migrate`, `serve` و قفل تک‌نمونه با `flock` — `serve` اکنون `http.Server` را (`internal/httpserver/server.go`، مسیر `/healthz`) پشت میان‌افزار امنیتی با `ReadHeaderTimeout`/`IdleTimeout` و خاموشی آرام اجرا می‌کند
- [x] **T1.6**: پکیج سیستم‌فایل امن `internal/fsx` با پوشش `os.Root`، `WriteNew`، `safename` و ۸ تست امنیتی سبز
- [x] **T1.7**: میان‌افزارهای امنیتی HTTP در `internal/httpserver` (بررسی Host/Origin/CSRF/CSP) با ۷ تست امنیتی سبز
- [x] **T1.8**: راه‌اندازی دیتابیس SQLite بدون CGO در `internal/store`، حالت WAL، و اسکریپت `0001_init.sql` با تست ایندکس‌های یکتا
- [x] **T1.9**: اسکیماهای JSON و قوانین بین‌فیلدی `internal/schema` با تست کامل

---

### فاز ۲ — ورود Roadmap و فهرست موضوعات (تکمیل‌شده ✅)
- [x] **T2.1**: پارسر قطعی Markdown با AST (`internal/roadmap/parse.go`) بر پایه `goldmark` — ۷ تست golden، تعیین‌پذیری ۱۰۰ اجرا، CRLF/BOM، حفظ نیم‌فاصله
- [x] **T2.2**: قواعد انتخاب «موضوع» (`internal/roadmap/select.go`): `deepest` (پیش‌فرض، عمیق‌ترین سطح سرفصل در کل فایل)، `level`، `items`؛ گروه‌های تو در تو فقط وقتی موضوعی دارند؛ زیرموارد به‌صورت لیست Markdown در `outline` — ۶ تست
  - ⚠️ توجه: فایل بدون سرفصل با قاعده پیش‌فرض صفر موضوع می‌دهد؛ پیش‌نمایش T2.4 باید پیشنهاد تغییر به `items` را نشان دهد
- [x] **T2.3**: شناسه پایدار (`internal/roadmap/stablekey.go`): `Normalize` (NFC، ی/ک عربی، حذف کشیده، نیم‌فاصله=فاصله، ارقام، حروف کوچک؛ برای `title_norm` هم استفاده می‌شود) و کلید ۱۶ هگز = sha256 مسیر نرمال‌شده گروه‌ها+عنوان؛ تکراری‌ها `#2`؛ تغییر نام ⇒ کلید جدید (تغییر نام گروه کلید موضوعات زیرش را هم عوض می‌کند) — ۵ تست؛ `Select` کلید گروه‌ها و موضوعات را پر می‌کند
- [x] **T2.4**: پیش‌نویس ورود (`internal/services/roadmaps`) و پیش‌نمایش درختی (`internal/httpserver/handlers_roadmaps.go`، قالب‌ها و `htmx.min.js` نسخه 2.0.4 محلی در `internal/ui`) — آپلود `.md` تا دقیقاً ۱ مگابایت (۴۱۳/۴۲۲)، تغییر قاعده و تیک‌ها با htmx (تغییر قاعده تیک‌ها را بازنشانی می‌کند)، پیشنهاد «آیتم‌های لیست» برای صفر موضوع، انقضای ۲۴ ساعته (پاک‌سازی هنگام شروع و هر آپلود)؛ تست خودکار: پیش از تأیید صفر رکورد در `roadmaps`/`groups`/`topics` و نبود `roadmaps/`؛ آزمون دستی مرورگر موفق
  - 🔧 اصلاح T1.7: `Referrer-Policy` از `no-referrer` به `same-origin` تغییر کرد؛ با `no-referrer` مرورگر در هر POST هم‌مبدأ `Origin: null` می‌فرستاد و همه فرم‌ها ۴۰۳ می‌شدند (سند طراحی هم اصلاح شد). بدنه بزرگ‌تر از سقف اکنون ۴۱۳ می‌دهد نه ۴۰۳ CSRF
- [x] **T2.5**: تأیید و ثبت تراکنشی (`internal/services/roadmaps/confirm.go`، `POST /roadmaps/drafts/{id}/confirm` با `confirm=1` وگرنه ۴۰۹) — یک تراکنش: حذف پیش‌نویس (جلوگیری از ثبت دوباره)، `roadmaps` (slug از نام فایل، یکتا در DB و دیسک: `go`، `go-2`)، `roadmap_revisions`، فقط گروه‌هایی که موضوع ثبت‌شده دارند، `topics` بدون موارد `Excluded` با `dir_name` = `NN-slug` (شماره سراسری = تعداد موضوعات موجود + ترتیب؛ slug حداکثر ۴۰ نویسه)؛ `rev-001.md` با `WriteNew` آخرین گام پیش از commit و در صورت شکست commit حذف می‌شود؛ پوشه `topics/` ساخته نمی‌شود — تست: شکست میانه با trigger و شکست نوشتن فایل ⇒ صفر رکورد و بدون فایل؛ صفر موضوع ⇒ ۴۲۲
- [x] **T2.6**: فهرست موضوعات (`internal/services/topics`، `GET /topics`، `/` ⇒ `/topics`، تأیید ورود ⇒ `303 /topics`) — درخت نقشه راه ← گروه‌ها (`<details>` جمع‌شونده) ← موضوعات به ترتیب سند؛ جست‌وجوی `?q=` با `roadmap.Normalize` و `instr(title_norm)` (ي/ك، نیم‌فاصله، حروف کوچک)، فیلتر `status` (مقدار ناشناخته نادیده گرفته می‌شود)، فقط شاخه‌های دارای نتیجه؛ برچسب وضعیت، نمره و «ناقص» از آخرین `attempt` مجموعه جاری، نشان «بایگانی‌شده» و «پیشنهادی»؛ جست‌وجوی زنده با htmx (`input delay:300ms`، `hx-push-url`)؛ حالت خالی با پیوند ورود — تست: ۵۰۰ موضوع p95 ≈ ۱۷ms (سقف ۳۰۰ms)، آزمون دود با سرور واقعی موفق
  - ⏭️ نشانگر «Job در حال اجرا» به فاز ۳ (T3.8) موکول شد؛ عنوان موضوع هنوز پیوند ندارد تا پنل موضوع (T3.8) ساخته شود
- [x] **T2.7**: ورود مجدد و گزارش تفاوت (`internal/services/roadmaps/diff.go`) — فرم آپلود فهرست «ورود مجدد: <نقشه راه>» دارد (`roadmap_id`، `?roadmap_id=` پیش‌انتخاب؛ نقشه ناموجود ⇒ ۴۲۲)؛ پیش‌نویس با قاعده ذخیره‌شده همان نقشه شروع می‌شود؛ گزارش تفاوت (جدید/تغییرکرده/بازگردانی/ناپدیدشده/بدون تغییر) با htmx همراه قاعده و تیک‌ها به‌روز می‌شود؛ حدس فازی = شباهت Levenshtein روی `title_norm` ≥ ۰٫۶ (فقط پیشنهاد؛ تغییر نام گروه ⇒ شباهت ۱)؛ هشدار RK8 در UI — تأیید: یک تراکنش، `rev-NNN.md` بعدی با `WriteNew`، `select_rule` به‌روز، گروه‌ها upsert، موضوعات هم‌کلید درجا به‌روز (عنوان/outline/ترتیب/گروه/`source_rev_id`؛ `dir_name` ثابت)، موضوعات بایگانی‌شده که برگردند فعال می‌شوند، ناپدیدشده‌ها (فقط `origin='roadmap'`) و گروه‌های بی‌موضوع فقط بایگانی — تست R11.2-1: شمارش رکوردها کم نمی‌شود و hash همه فایل‌های `topics/` و `roadmaps/` قبل/بعد یکسان است؛ چرخه بایگانی ⇐ بازگردانی؛ تست HTTP کامل
  - ⚠️ ستون `import_drafts.diff` استفاده نمی‌شود: تفاوت هر بار از محتوا + DB محاسبه می‌شود (تعیین‌پذیر، بدون داده کهنه)

---

### فاز ۳ — هسته Job Runner و تولید درس (فاز فعال کنونی 🚀)
- [x] **T3.1**: اینترفیس `ai.Adapter` (`internal/ai`: انواع، ۸ خطای نوع‌دار، `ai.Code` ⇒ `error_code`) و `ai/antigravity` — `argbuilder` بدون شل (فهرست سیاه پرچم‌ها با `-x`/`--x`/`=`: `dangerously-skip-permissions`، `mode`، `add-dir` و پرچم‌های تحت کنترل Adapter)؛ نوشتن `output.schema.json` در `WorkDir` و `cwd=WorkDir`؛ پارسر NDJSON (`event`/`result`، خطوط غیر JSON نادیده)؛ نگاشت خطا با الگوهای قابل پیکربندی (`Config.Patterns`؛ «Eligibility» ⇒ `auth_required`)؛ timeout = `--print-timeout` + kill پس از `KillGrace` (۳۰s)، لغو ⇒ `ErrCanceled`؛ لاگ `.app/logs/jobs/<id>/<UTC>-<task>/` (`prompt.txt`, `events.ndjson`, `response.json`, `stderr.txt`, `meta.json`) با mask رازها؛ `Probe` (نسخه، پشتیبانی stream-json/json-schema، مدل‌ها، احراز) — آزمون قراردادی: باینری تست خودش نقش `agy` جعلی را بازی می‌کند (موفق، fallback، eligibility واقعی، rate limit، crash، خروج بی‌نتیجه، خروجی بی‌ساختار، کند ⇒ timeout/لغو)
  - ⏳ **باز:** اجرای واقعی دستی (مسدود به‌علت محدودیت مکانی حساب) — بالا را ببینید
  - ⏭️ نگهبان سکوت (`StallTimeout`) در T3.5. اعتبارسنجی نام مدل با `agy models` و پیام‌های فارسی خطا در T3.9
- [x] **T3.2**: `internal/ai/procgroup` — `Start(cmd)` فرزند را در گروه پردازش (یونیکس: `Setpgid`؛ `Kill` = SIGTERM به `-pgid` و SIGKILL پس از ۳s حتی اگر رهبر خارج شده باشد) یا Job Object (ویندوز: `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`؛ `Kill` = `TerminateJobObject` + `Process.Kill`) اجرا می‌کند و `cmd.Cancel` را جایگزین می‌کند؛ `Close` پس از `Wait` بازمانده‌ها را می‌کشد؛ Adapter آنتی‌گراویتی از آن استفاده می‌کند — تست: فرزند + نوه (نوه SIGTERM را نادیده می‌گیرد) ⇒ لغو، هر دو ظرف ≈۳s تمام؛ فرزندی که نوه یتیم رها کرده ⇒ `Close` نوه را می‌کشد؛ آزمون منفی (kill فقط فرزند) هر دو تست را می‌شکند؛ `GOOS=windows go vet` سبز
  - ⏳ **باز:** آزمون دستی ویندوز (درخت واقعی `agy` و بستن ناگهانی `hub.exe`) هنوز ثبت نشده
  - ⚠️ ویندوز: نوه‌ای که بین `CreateProcess` و `AssignProcessToJobObject` ساخته شود از Job فرار می‌کند (پنجره میکروثانیه‌ای). یونیکس: crash سخت `hub` گروه را زنده رها می‌کند (خاموشی عادی با لغو context پوشش داده می‌شود؛ T3.4 باید context ریشه را هنگام خاموشی لغو کند)
- [x] **T3.3**: `internal/ai/prompt` — `Composer.Compose(Input)` پوشه `.app/work/<job>` را از صفر می‌سازد (بقایای اجرای crash‌شده پاک می‌شود) و فقط فهرست سفید §6.3 را می‌نویسد: `AGENTS.md`، `SKILL.md`، `profile.md` و `inputs/{task.json, roadmap_excerpt.md, lesson.md, question.md, rubric.json, answer<ext>}` (نام دیگر ⇒ `ErrInput`)؛ ورودی‌ها **بایت** هستند نه مسیر، پس sandbox همیشه کپی دارد؛ `name`/`version` از frontmatter (BOM/CRLF مجاز) ⇒ `SkillRef`؛ `Preamble` امنیتی سه‌قاعده‌ای در `-p` و بلافاصله پس از frontmatter در `SKILL.md`؛ Prompt بیش از ۴KB ⇒ خطا؛ `output.schema.json` را همچنان Adapter می‌نویسد؛ `Cleanup(job)` با `fsx.Root.RemoveAll` (جدید) — `internal/ai/fake` با سناریوهای `ok` و `injection` (همه فایل‌های WorkDir را بازنویسی و `evil.sh` اضافه می‌کند؛ خروجی شامل `../../x` و `<script>`) — تست: فهرست دقیق فایل‌های sandbox، hash همه فایل‌های واقعی پایه (بیرون از `.app/`) قبل/بعد injection یکسان، پیش‌گفتار و سقف ۴KB برای هر سه Skill واقعی، رد نام‌های غیرمجاز و Skill بی‌frontmatter/بی‌نسخه بدون باقی‌گذاشتن sandbox
  - ⏭️ خواندن `AGENTS.md`/`profile.md`/`.agent/<skill>/SKILL.md` از دایرکتوری پایه و ساخت `task.json` با Handlerها (T3.6+)؛ سناریوهای دیگر Adapter جعلی (`slow`، `invalid_json`، ...) در T3.4/T3.5
- [x] **T3.4**: `internal/jobs` — `Runner` با یک dispatcher (بیدارشدن با channel هنگام Enqueue + ticker ۲s) و سمافور `jobs.max_concurrent`؛ برداشت FIFO (`ORDER BY enqueued_at, id`) با CAS `queued→running`؛ `Enqueue` درون تراکنش immediate وجود Job فعال روی scope را بررسی می‌کند و `*BusyError{ActiveID}` (`errors.Is` ⇒ `domain.ErrScopeBusy`) برمی‌گرداند (ایندکس `uq_jobs_active_scope` پشتیبان است)؛ `Handler func(ctx, Job) error` به‌ازای `JobKind` ثبت می‌شود؛ نتیجه: `nil` ⇒ `succeeded`، خطا ⇒ `failed` با `ai.Code` (خطای غیر AI ⇒ `internal`)، panic ⇒ `failed` (سرور نمی‌افتد)، Kind بی‌Handler ⇒ `failed`؛ heartbeat هر ۵s؛ `Recover` در شروع `Run`: `running` ⇒ `failed/interrupted` با پیام «برنامه هنگام اجرا بسته شد؛ قابل تکرار است»، `queued` ادامه می‌یابد؛ `Retry(id)` فقط برای `failed` ⇒ Job جدید با `retry_of`؛ خاموشی: لغو context ریشه ⇒ Jobهای در حال اجرا `failed/interrupted` (نوشتن نهایی با `WithoutCancel`) و `Run` تا پایان workerها صبر می‌کند؛ `hub serve` آن را اجرا و پیش از بستن DB متوقف می‌کند — تست (`-race`): scope مشغول، FIFO با `max=1` و اوج همزمانی ۱، ثبت خطا/panic/بی‌Handler، بازیابی crash + retry، خاموشی، heartbeat؛ آزمون دود با باینری واقعی موفق
  - ⏭️ لغو کاربر (`cancel_requested`، `canceled`)، timeout، stall و commit غیرقابل لغو در T3.5؛ `phase`/`progress` و پیام‌های فارسی خطا با Handlerها (T3.6) و T3.9؛ هنوز هیچ Handlerی در `serve` ثبت نشده (T3.6)
- [x] **T3.5**: `Runner.Cancel(id)` — `queued` ⇒ همان لحظه `canceled` (CAS برداشت مانع اجرایش می‌شود)؛ `running` ⇒ `cancel_requested=1` سپس لغو context همان Job (پرچم پس از ثبت cancelFn دوباره خوانده می‌شود تا لغوِ هم‌زمان با برداشت گم نشود)؛ وضعیت پایانی دیگر ⇒ `ErrInvalidState`، ناموجود ⇒ `ErrNotFound`؛ خروجی `late` اگر Job در فاز commit باشد — `Job.Commit(ctx, fn)`: دروازه اتمیک `phase='commit' WHERE cancel_requested=0` (اگر لغو زودتر رسیده ⇒ `ErrCanceled` و commit اجرا نمی‌شود)، سپس `fn` با `context.WithoutCancel`؛ لغوی که حین commit برسد ⇒ `succeeded` با پیام «لغو دیر رسید…» — نتیجه: خطا + `cancel_requested` ⇒ `canceled`؛ `Runner.Files` پوشه‌های `.app/work/<id>` و `.app/staging/<id>` (`jobs.StagingRel`) را پس از `succeeded`/`canceled` پاک می‌کند و برای `failed` نگه می‌دارد — `ai.Watch(ctx, adapter, req, events)`: نگهبان سکوت عمومی با `StallTimeout` (هر رویداد تایمر را reset می‌کند؛ سکوت ⇒ لغو context و kill درخت پردازش ⇒ `ErrStalled`/`stalled`)؛ timeout همان `ErrTimeout` آداپتر ⇒ `failed(timeout)` — سناریوهای `slow` و `hang` در `ai/fake` (احترام به ctx و `req.Timeout`) — تست (`-race`): لغو `slow` ظرف ≤۵s ⇒ `canceled`، `work/`/`staging/` پاک، `lesson.md` ساخته نشده؛ لغو Job صف؛ timeout ⇒ `failed(timeout)` بدون فایل؛ stall ⇒ `failed(stalled)`؛ لغو حین commit ⇒ `succeeded` + پیام + فایل کامل؛ لغو پیش از commit حتی با Handler بی‌اعتنا به ctx ⇒ commit اجرا نمی‌شود (آزمون جهشی: حذف دروازه این تست را می‌شکند)
  - ⏭️ مسیر HTTP `POST /jobs/{id}/cancel` و `retry` با کارت Job در T3.8؛ Handlerها باید فراخوانی AI را با `ai.Watch` و `StallTimeout = cfg.Jobs.StallTimeout` انجام دهند و commit را در `j.Commit` (T3.6)؛ سقف زمان کل Job (ارزیابی ۳۰ دقیقه) با T5.2
- [x] **T3.6**: `internal/services/lessons` — `Service.Handle` (ثبت‌شده در `hub serve` با Adapter آنتی‌گراویتی از `cfg.Adapter`): **prepare** موضوع، گروه، نقشه راه و موضوعات قبلی/بعدی فعال (بر اساس `ord`) ⇒ `inputs/task.json` + `roadmap_excerpt.md`؛ `AGENTS.md`/`profile.md`/`.agent/lesson-generator/SKILL.md` از دایرکتوری پایه؛ ثبت `inputs_fingerprint`، adapter/model/skill/skill_version؛ درس جاری بدون `params.confirm_regenerate=true` ⇒ `failed` پیش از AI — **ai** با `ai.Watch` (`timeout_lesson`، `stall_timeout`)؛ `usage`/`log_dir` — **validate** اسکیما ⇒ `invalid_output`؛ کلیدواژه/پیشنهاد خالی حذف و یک‌خطی — **commit** در `j.Commit`: `commit_journal`؛ نسخه اول با `WriteNew`؛ بازتولید: ابتدا کپی محتوای روی دیسک به `lesson/history/lesson.NNNN.md` (NNNN = شماره نسخه قبلی) با `WriteNew` (فایل هم‌محتوای باقی‌مانده از crash پذیرفته می‌شود، غیرهم‌محتوا هرگز بازنویسی نمی‌شود) سپس `WriteAtomic`؛ یک تراکنش: ردیف `files` جدید برای history و انتقال `file_id` نسخه قبلی به آن، ردیف `lesson.md` برای نسخه جدید به‌روز، `lesson_versions` جدید `is_current=1`، `suggestions` با `pending` و `ON CONFLICT DO NOTHING` (تکراری با `title_norm`)، `topics.status` از `created` ⇒ `content_ready`، `result_ref`؛ شکست تراکنش ⇒ `lesson.md` قبلی بازگردانی و history ساخته‌شده حذف (نسخه اول: `lesson.md` حذف) — بخش «## کلیدواژه‌ها» را نرم‌افزار در انتهای فایل می‌سازد — پکیج `skills` (embed) و `skills.Install` در شروع `serve`: فایل‌های ناموجود را با `WriteNew` نصب می‌کند، نسخه کاربر بازنویسی نمی‌شود — تست (`-race`): درس اول (فایل، کلیدواژه در انتها، ردیف‌ها، پیشنهاد تکراری حذف، وضعیت موضوع، پاک‌شدن work)؛ بازتولید بدون/با تأیید (hash history = v1، ردیف‌ها، دو ردیف `files`)؛ شکست تراکنش با trigger ⇒ فایل‌ها بازگردانی (آزمون جهشی: حذف undo تست را می‌شکند)؛ خروجی injection ⇒ `invalid_output` بدون فایل؛ محتوای `task.json`؛ آزمون دود `serve` (نصب ۵ فایل)
  - ⏭️ retry خودکار یک‌باره برای `invalid_output` با T4.1 (سناریوی `invalid_then_valid`)؛ ساخت خروجی در `.app/staging` برای درس لازم نشد (یک فایل در حافظه)؛ بازیابی `commit_journal` پس از crash بین نوشتن فایل و تراکنش هنوز پیاده نشده (نسخه قبلی در history حفظ می‌شود ولی ممکن است `lesson.md` جلوتر از DB باشد) — با همگام‌سازی/T6؛ Enqueue از UI و `confirm_regenerate` در T3.8
- [x] **T3.7**: `internal/render` — `Markdown(src) template.HTML`: goldmark (Table با `align`، Strikethrough، Linkify؛ بدون `WithUnsafe` ⇒ HTML خام حذف) + `bluemonday` با سیاست فهرست سفید (لایه دوم؛ فقط `http`/`https`/`mailto` و نسبی؛ `rel="noreferrer noopener"` + `target="_blank"` برای لینک‌های بیرونی)؛ AST transformer به `p`/`h*`/`li`/`blockquote`/`th`/`td` مقدار `dir="auto"` می‌دهد و همه تصاویر را حذف می‌کند؛ `<pre>`/`<code>` با `dir="ltr"` (+ CSS `unicode-bidi: plaintext`)؛ `StripBidi` کاراکترهای embedding/override/isolate را پیش از رندر حذف می‌کند (ZWNJ و LRM/RLM حفظ) — تست: golden (فارسی + انگلیسی + کد درون‌خطی، لیست، جدول، بلوک کد، لینک‌ها، تصویر)، ۲۸ payload XSS (آزمون جهشی: بدون هر دو لایه ۲۲ نشت)، حذف BiDi
  - ⏭️ اتصال به تب درس در T3.8؛ نمایش ارقام فارسی (گزینه تنظیمات) با T3.9
- [ ] **T3.8**: پنل موضوع و کارت پیشرفت زنده Job با htmx
- [ ] **T3.9**: صفحه تنظیمات و خروجی doctor

---

### فاز ۴ — تولید تمرین، مانیفست و تمپلیت پاسخ (در انتظار)
- [ ] **T4.1**: هندلر تولید تمرین و فایل‌های `exercises.md`, `manifest.json`, `rubric-vN.json`
- [ ] **T4.2**: اعتبارسنجی جمع امتیاز ۱۰۰ برای معیارهای هر تمرین
- [ ] **T4.3**: ساخت فایل‌های پاسخ با کامنت‌های متناسب زبان و رد تراکنش در صورت وجود فایل قبلی
- [ ] **T4.4**: نسخه‌بندی تمرین‌ها (`vN+1`) با حفظ تمام فایل‌های قبلی
- [ ] **T4.5**: رابط کاربری تب تمرین‌ها و پاسخ‌ها با امکان کپی آسان مسیر فایل
- [ ] **T4.6**: مدیریت وابستگی‌های دانشی خارج از سرفصل (`out_of_scope_knowledge`)

---

### فاز ۵ — موتور ارزیابی، نمره‌دهی و گزارش‌ها (در انتظار)
- [ ] **T5.1**: انتخاب ورودی‌های ارزیابی (حالت افزایشی و عدم ارسال فایل‌های بدون پاسخ به AI)
- [ ] **T5.2**: ارزیابی تمرین‌به‌تمرین با چک‌پوینت‌های `job_steps`
- [ ] **T5.3**: پکیج نمره‌دهی قطعی `scoring` بر اساس میانگین وزنی
- [ ] **T5.4**: تولید گزارش‌های `qNN-report.md`, `summary.md`, `result.json` با وفاداری بایت‌به‌بایت به پاسخ کاربر
- [ ] **T5.5**: ثبت تلاش جدید در `reports/vN/attempt-K` با برچسب تغییرناپذیر
- [ ] **T5.6**: محافظ‌های تقلب و ناهنجاری نمره
- [ ] **T5.7**: رابط کاربری تب کارنامه و نمرات
- [ ] **T5.8**: کالیبراسیون و سنجش دقت مهارت ارزیابی با ۵ نمونه پاسخ متنوع

---

### فاز ۶ — پیشنهادها، همگام‌سازی و انتشار نهایی (در انتظار)
- [ ] **T6.1**: گردش کار پذیرش/رد پیشنهادهای تکمیلی و پیش‌نیاز
- [ ] **T6.2**: تحلیل هوش مصنوعی برای نقشه‌های مبهم در پیش‌نمایش
- [ ] **T6.3**: سرویس همگام‌سازی فایل‌های دیسک (`syncsvc`)
- [ ] **T6.4**: بازمحاسبه وضعیت موضوعات پس از همگام‌سازی
- [ ] **T6.5**: پاک‌سازی محیط و تهیه پشتیبان دیتابیس با `VACUUM INTO`
- [ ] **T6.6**: دکمه باز کردن فایل در ادیتور کاربر
- [ ] **T6.7**: ویزارد راه‌اندازی اولیه (First-run)
- [ ] **T6.8**: دستور `hub rebuild` جهت بازسازی دیتابیس از روی فایل‌ها
- [ ] **T6.9**: تست سرتاسری معیارهای ۹‌گانه R11.2 و ساخت باینری مستقل `hub.exe`

---

## 🔒 قوانین معماری تغییرناپذیر (Architectural Commandments)

1. **فایل‌های کاربر هرگز بازنویسی نمی‌شوند:** هر فایل پاسخ یا گزارش جدید باید با `fsx.WriteNew` ایجاد شود.
2. **هوش مصنوعی دسترسی دیسک ندارد:** هوش مصنوعی فقط JSON تولید می‌کند و تمام فایل‌ها توسط نرم‌افزار ساخته می‌شوند.
3. **یک کار فعال در هر موضوع:** تضمین در سطح دیتابیس با ایندکس `uq_jobs_active_scope`.
4. **انحصار به لوپ‌بک:** سرور فقط روی `127.0.0.1:8787` اجرا می‌شود و درخواست‌های خارجی یا بدون CSRF رد می‌شوند.
5. **بدون CGO:** دیتابیس SQLite منحصراً با `modernc.org/sqlite` بدون نیاز به کامپایلر C بیلد می‌شود.
