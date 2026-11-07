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
| Arcus Hold Test (AAPL) | `debot-xvenue-hedge-arcus` | `experimental` | #1228 (旧 #1123)。Arcus long / Core short の hedged hold が Arcus points を生むかの 1 週間テスト (AAPL、約 $20k/leg、2026-10-25 → 11-04 の drop 後に DISARM)。判定は #1228 の gate (11-04 drop で 0x812B ≥ 10 pt)。KPI (cost / point) は未設定 |
| Arcus presence (SPY) | `debot-arcus-vol` | `subsidy` | #1093。Arcus points。cost = 純損失、KPI = cost / point |
| Arcus presence (GLD) | `debot-arcus-vol-gld` | `subsidy` | #1093 |
| Arcus presence (QQQ) | `debot-arcus-vol-qqq` | `subsidy` | #1093 |
| Arcus presence (NVDA) | `debot-arcus-vol-nvda` | `subsidy` | #1093 |
| Han Bridge (Engine B) | `engine-b-live` | `alpha_candidate` | #866。gate 未通過 |
| XSMOM | `book-runtime-xsmom-695` | `alpha` | #695。2026-10-08 readout VERDICT GO で α 候補から昇格 (taxonomy §4.4)、live $1000 (10-08〜)。PnL / equity を表示する |
| XSMOM shadow track | `xsmom-695-shadow` | `alpha_candidate` | #695 の shadow-paper ledger。**2026-10-02 の gate を判定するのはこちら** (#964)。gate 進捗のみ、PnL は出さない |

`alpha` は事前登録した readout に合格した α 候補の行き先 (taxonomy §4.4)。benchmark は α 候補と同じだが、gate が閉じているので成績を伏せない。ブラインドは bucket が `alpha_candidate` のときだけ。paper / live の区別は bucket ではなく `status.dry_run` / `pnl_source` で表示する。

`experimental` は補助金の**取り方そのもの**を確かめる事前登録済みの短期テスト (例: #1228 — 建玉の保有で Arcus points が付くか)。資金配分ではないので subsidy の集計 (cost / unit) から外し、判定はその issue の gate で行う。結果が出たら subsidy に移すか停止する。

表に無い service は `unclassified` として描画され、集計から外れる。
新しい bot を足すときは bot-strategy の台帳 → この表 → `taxonomyBuckets` の順に更新する。

## 例外: 上部パネルの Total equity (2026-11-07 オーナー決定)

上の「バケツをまたいだ合算には意味がない」は**成績** (PnL・リターン) の話。上部の fleet summary に 1 つだけ置く
`Total equity` は成績ではなく**資本** — いま各 bot の口座にいくら入っているか — で、オーナーの依頼でバケツ横断
(`unclassified` の LLP monitor も含む) で合算する。PnL・リターンには決して使わない。

- **口座ごとに 1 回**: 同じ口座を共有する target は `config.yaml` で同じ `equity_account:` ラベル (口座 id や
  アドレスではなく任意の名前) を付ける。同じラベルの target は最新の snapshot 1 つだけを数える。ラベル無しの
  target は 1 target = 1 口座。status にはわざと口座 id を出していないので、ラベルが唯一の重複除去キー。
- **実弾のみ**: `dry_run: true`、`pnl_source` が paper / shadow / sim の帳簿は除外。
- **α 候補**: 成績は伏せたまま。solvency 例外として既に出している venue equity (Han Bridge の
  `han_bridge.venue_equity_usd`) だけを数える。
- **LLP package**: #1255 readout と同じ NAV = LLP 時価 + 保有 LIT (stake 分 + 未 stake 分) × LIT 価格 + perp collateral +
  perp 含み損益 + spot USDC。
- **Arcus volume bot の wallet** (owner 2026-11-07): runtime の status に口座価値が無いので、`arcus_vol.account_address`
  (+ `account_index`、既定 0) を設定した target について dashboard server が venue の公開 account endpoint
  (`GET /v1/account?address=&accountIndex=`、鍵・署名なし、60 s cache) から equity を読む。アドレスは server の外に
  出さず、一方向の `account_key` だけを返す → 同じ wallet の target (SPY / GLD …) は 1 口座。鮮度は wallet の読み取り
  時刻で判定する (止めた market の古い status で wallet を stale にしない)。**別 target が既に数えている wallet**
  (例: #1228 の hedge holder の Arcus leg = 0x812B) に volume bot target を置くときは `account_address` の代わりに
  `equity_counted_by: <その target 名>` を書く (二重計上防止。存在しない名前は起動時エラー、両方の指定もエラー)。
- **口座価値を出さない target** (アドレス未設定の Arcus volume bot など) は 0 と見なさず「Not counted」に列挙する。stale / 欠損は最後の値を
  使い、合計に `partial` を付ける。内訳 (venue 別・バケツ別) は tooltip。24 h 変化は出さない (target ごとの履歴が揃わない)。
