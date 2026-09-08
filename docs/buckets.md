# Return-source buckets (dashboard mirror)

**Source of truth**: `bot-strategy` の `docs/return-source-taxonomy.md` §3
(bot-strategy#954)。この表はそのミラーであり、**分類を変えるときは先に
bot-strategy 側の台帳を更新する**。ダッシュボード側の変更が先行してはいけない。

各 target がどのバケツかで benchmark が変わる (α 候補 → コスト控除後ゼロ、
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
| Robinhood Freq / B | `debot-pair-robinhood-lighter` | `subsidy` | #798 / #938。points 捕獲。signal の α はゼロ判定 (#935) |
| Arcus SPY/QQQ | `arcus-spot-live-tick` | `subsidy` | #902 / #938。activity → Perps アクセス。price edge 無し (0/362 pairs) |
| Han Bridge (Engine B) | `engine-b-live` | `alpha_candidate` | #866。gate 未通過 |
| XSMOM | `book-runtime-xsmom-695` | `alpha_candidate` | #695。readout 2026-10-02、途中の PnL は出さない (#958) |

表に無い service は `unclassified` として描画され、集計から外れる。
新しい bot を足すときは bot-strategy の台帳 → この表 → `taxonomyBuckets` の順に更新する。
