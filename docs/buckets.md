# Return-source buckets (dashboard mirror)

**Source of truth**: `bot-strategy` の `docs/return-source-taxonomy.md` §3
(bot-strategy#954)。この表はそのミラーであり、**分類を変えるときは先に
bot-strategy 側の台帳を更新する**。ダッシュボード側の変更が先行してはいけない。

各 target がどのバケツかで benchmark が変わる (α 候補・α → コスト控除後ゼロ、
β → 同額の現物 buy & hold、subsidy → 補助金 1 単位あたりのコスト)。
バケツをまたいだ equity / PnL の合算には意味がないため、ダッシュボードは
バケツごとに集計する (bot-strategy#959)。

`bucket` 列がコード・設定で使う機械可読値。`buckets.go` の `taxonomyBuckets`
はこの表のミラーで、`TestBucketRegistryMatchesDoc` が両者の一致を検証する。
`config.yaml` の target は `bucket:` を省略すれば service 名からこの表を引く。
明示した場合はこの表と一致していなければ起動時に設定エラーになる。

| ターゲット | service | bucket | 備考 |
|---|---|---|---|
| Bull-holder | `debot-bull-holder` | `beta` | #893 / #909 / #910。benchmark は spot buy & hold (#955) |
| HYPE Accumulator | `hype-accumulator` | `beta` | #849。β + carry — staking 利回りは価格 β と分けて計上する (#956) |
| Robinhood Freq / B | `debot-pair-robinhood-lighter` | `subsidy` | #798 / #938。points 捕獲。signal の α はゼロ判定 (#935)。2026-09-19 停止 (#1046、bleed) |
| Robinhood Hedge | `debot-xvenue-hedge-holder` | `subsidy` | #1046。RH long / Core short の hedged hold で points 捕獲。cost = ARM 以降の equity 変化 |
| Arcus×Core QQQ hedge | `debot-xvenue-hedge-arcus` | `subsidy` | #1123。Arcus long / Core short (QQQ) の hedged hold が Arcus points を生むかの G0。**2026-10-07 停止** (DISARM・両脚 close、wallet は #1093 に転用)。Arcus points は未収集のため KPI (cost / point) は未設定 |
| Arcus presence (SPY) | `debot-arcus-vol` | `subsidy` | #1093。Arcus points。cost = 純損失、KPI = cost / point |
| Arcus presence (GLD) | `debot-arcus-vol-gld` | `subsidy` | #1093 |
| Arcus presence (QQQ) | `debot-arcus-vol-qqq` | `subsidy` | #1093 |
| Arcus presence (NVDA) | `debot-arcus-vol-nvda` | `subsidy` | #1093 |
| Han Bridge (Engine B) | `engine-b-live` | `alpha_candidate` | #866。gate 未通過 |
| XSMOM | `book-runtime-xsmom-695` | `alpha` | #695。2026-10-08 readout VERDICT GO で α 候補から昇格 (taxonomy §4.4)、live $1000 (10-08〜)。PnL / equity を表示する |
| XSMOM shadow track | `xsmom-695-shadow` | `alpha_candidate` | #695 の shadow-paper ledger。**2026-10-02 の gate を判定するのはこちら** (#964)。gate 進捗のみ、PnL は出さない |

`alpha` は事前登録した readout に合格した α 候補の行き先 (taxonomy §4.4)。benchmark は α 候補と同じだが、gate が閉じているので成績を伏せない。ブラインドは bucket が `alpha_candidate` のときだけ。paper / live の区別は bucket ではなく `status.dry_run` / `pnl_source` で表示する。

表に無い service は `unclassified` として描画され、集計から外れる。
新しい bot を足すときは bot-strategy の台帳 → この表 → `taxonomyBuckets` の順に更新する。
