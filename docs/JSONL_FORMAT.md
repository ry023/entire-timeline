# Timeline JSONL v1

`entire timeline <range> --format jsonl`の公開出力仕様です。Entireが保存するAgent固有の`full.jsonl`やcompact transcript、要約保存用JSONLとは別の形式です。

- [JSON Schema](../schemas/timeline.v1.schema.json)（Draft 2020-12）
- [架空データのサンプル](examples/timeline.v1.jsonl)

## ファイルと順序

UTF-8で、各行が1つのJSONオブジェクトです。配列やヘッダー行はありません。各レコードの末尾にLFを出力し、本文中の改行はJSON文字列内でエスケープします。イベントがなければ0バイトの出力です。

Schemaはファイル全体ではなく各行に適用します。空出力は有効です。検証器では`date-time`のformat検証を有効にしてください。Schemaはローカル参照だけで構成されており、検証にネットワークは不要です。

**出力順が表示順です。** 利用側はその順序を保ってください。時刻不明・逆行時刻・欠落マーカーもあるため、`timestamp`での再ソートは推奨しません。異なるCheckpointに同じ発言が記録されていれば両方を残します。IDや本文による重複排除も推奨しません。`event_id`は全レコードに存在する一意キーではありません。

## 通常イベント

| フィールド | 必須 | 意味 |
| --- | --- | --- |
| `session_id` | ○ | 元SessionのID |
| `agent` | ○ | Agent名。現在は`copilot-cli` / `claude-code` |
| `timestamp` | ○ | RFC 3339日時（小数秒あり得る）。不明なら`null`。整列用の補完時刻は出力しない |
| `type` | ○ | 元イベントの種別。未知の種類も保持 |
| `role` | ○ | 表示用のRole。通常は`user` / `assistant` / `tool` / `system` |
| `source_line` | ○ | 取得元Checkpointの`full.jsonl`内の0始まりの物理行番号 |
| `checkpoints` | ○ | 通常は`checkpoint_id`と同じIDを1件、補助会話なら空配列 |
| `checkpoint_id` | 通常のみ | イベントの担当Checkpoint。補助会話では省略 |
| `event_id` | — | 元のID / UUID。ない場合は省略し、合成しない |
| `parts` | — | 表示用のcontent block配列。順序を保持する |
| `raw` | — | 元イベント。`--filter conversation`では省略 |
| `summary` | — | 本文と一致する保存済み要約があれば付与 |
| `attribution` | 補助会話のみ | 下記の出典情報 |

`parts[]`は`kind`（必須の文字列）、`text`・`name`（省略可能な文字列）、`data`（省略可能な任意JSON値）からなります。現在の`kind`は`text`・`reasoning`・`tool_call`・`tool_result`・`data`です。空の`text`や`name`は省略されることがあります。`raw`・`data`内のAgent固有スキーマはこの仕様の対象外です。

`--include-unattributed`で追加した補助会話は`checkpoint_id`を省略し、`checkpoints`を空配列にします。`attribution`は`status: "unattributed"`、取得元の`source_checkpoint_id`、境界の`previous_checkpoint_id` / `next_checkpoint_id`、近くの欠落ID配列`nearby_missing_checkpoint_ids`を持ちます。これらは全て必須です。境界情報は担当Checkpointへの所属や指定コミット範囲への所属を証明しません。

## 欠落レコード

`type: "timeline.error"`、`role: "system"`、`error`を持つ専用レコードです。通常イベントの出典情報（`session_id`・`timestamp`・`source_line`等）は付けません。未知の元イベントが同じ`type`文字列を持つ場合もあるため、通常イベントとの判別には`error`の有無も使ってください。

`error`内の必須項目は次のとおりです。

| フィールド | 意味 |
| --- | --- |
| `code` | 現在は`checkpoint_unavailable`。ローカルで取得できない意味で、リモートでの欠落を確認した意味ではない |
| `checkpoint_id` | 欠落ID |
| `message` | 説明文。プログラムの分岐には使わない |
| `commits` | 参照元コミットの配列。各要素に`sha`とRFC 3339の`timestamp` |
| `placement` | 表示位置を決めた根拠 |

`previous_checkpoint_id` / `next_checkpoint_id`は分かる場合だけ付与します。`placement`の現在の値は`after_previous_checkpoint`・`before_next_checkpoint`・`git_commit_time_fallback`・`end_without_conversation_anchor`です。会話上の正確な位置は保証しません。`commits[].timestamp`はGitのcommitter時刻で、会話時刻ではありません。

## フィルタと外部ビュー

`--filter all`（既定）と`--filter conversation`は同じSchemaに適合します。conversationはUser／Assistant本文だけを`parts`に残し、`raw`を省略します。欠落レコード・表示対象本文の要約と出典情報は残ります。`--include-tools`はJSONLの内容を変更しません。

HTMLエクスポーターは`parts`を主な表示入力にし、Agentごとの`raw`解析に依存しない構成にしてください。未知の`kind`・イベントは名前とデータを汎用表示し、黙って捨てないことを推奨します。Markdown・任意データは外部入力としてエスケープやサニタイズして表示してください。欠落と紐づき未確定の印も残してください。

指定したGit範囲、全体の件数、Sessionラベル対応表、stderrの警告はJSONLに含まれません。通常レコードにはコミットSHAもありません。JSONLだけからこれらを完全に再構成できるとは限りません。

## 互換性

v1はSchemaのファイル名と`$id`（`urn:entire-timeline:jsonl:v1`）で識別します。レコード内にバージョンフィールドはありません。利用側はv1を明示的に選び、再現可能な検証にはタグやコミットを固定したSchemaを使ってください。

未知の追加フィールドと文字列の種類は許容します。既存フィールドの削除・型や意味の変更、必須項目の追加など、従来の利用側と互換でない変更は別メジャー版とします。Schemaによる型検証に加え、`checkpoints`と`checkpoint_id`の値の一致などの意味的な制約は出力テストで検証します。

`make test`で実際の復元・描画結果と公開サンプルを検証します。検証ライブラリはテストでのみ使用し、CLI実行時にSchema検証は行いません。
