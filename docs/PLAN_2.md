# 第2段階MVP：発言ごとの要約生成・Git保存・リモート同期

作成日：2026-10-22

この文書は、[PLAN.md](PLAN.md)に基づいて完成した会話復元・表示機能に、要約機能を追加するための議論と合意をまとめたものです。既存のPLAN.mdは変更しません。

本書は次の実装計画です。記載する要約生成・保存・同期・hook用のCLIは、現時点では未実装です。合意済みの方針と、今後決める事項を区別します。

## 1. 目的と今回の対象

Entireから復元した会話の各発言に、人間が読み返すための1行要約を付けます。要約をSession単位で保存し、必要に応じてリモートへ共有できるようにします。

今回の対象は以下です。

- User／Assistantの会話本文の要約。
- Entire本体の設定を参照したprovider・modelの選択。
- Session別JSONLによる要約の永続化。
- 作業リポジトリのローカルGit内にある、timeline専用メタデータブランチへの保存。
- Entire本体と同じCheckpoint同期先へのpush／fetch。
- Git hookから同期を起動できるようにするための、timeline側の登録処理。

元の会話は引き続きEntireを正とします。要約で本文を置き換えず、既存の全文表示とJSONLの元イベント保持を維持します。

**第2段階MVPの初期対応はGitHub Copilot CLI（`copilot-cli`）に限定します。** 会話データの解析と要約生成providerの両方をCopilot CLIから始め、Claude Code・Codex等への要約対応は今回の対象外とします。既存のClaude Codeの復元・表示機能を削除する変更は行いません。

## 2. 要約の単位と入力

### 2.1 合意した対象

**1発言につき1行要約**とします。Userの依頼とAssistantの応答を、ターン全体やSession全体の1件の要約にまとめる方式ではありません。

| 元データ | 要約の扱い |
| --- | --- |
| Userの依頼・質問 | 発言ごとに要約する |
| Assistantの説明・応答 | 発言ごとに要約する |
| Tool Call／Tool Resultのみのイベント | 要約しない |
| CoT・reasoning・thinkingのみのイベント | 要約しない |
| 本文とTool／CoTが混在したイベント | 会話本文だけを抽出して要約する |
| 1発言内の複数テキストブロック | 本文を欠落させず、1発言としてまとめて要約する |

Tool／CoTは表示時に隠すだけでなく、**要約providerへ渡す対象本文から除外**します。元イベント自体は復元結果・JSONLに保持します。

### 2.2 現在の実装で変更が必要な点

現在の`internal/timeline/decode.go`は、Copilotの`reasoningText`を、会話本文と同じ`Part.Kind = "text"`にまとめています。そのため、現状の`parts`からtextだけを選んでもCoTを除外できません。

まずCopilotのネイティブJSONLと、既存MVPで対応したEntire compact v1形式について、会話本文とCoTを区別して保持し、要約用の本文抽出処理を実装します。`raw`に残る元の構造も利用できます。元データで会話本文とCoTが区別されていない場合、本文の意味から推測して削除する処理は先行実装しません。

### 2.3 既存要約の扱い

- 既存の有効な要約を保持し、選択した対象の未要約発言をまとめて埋める方針です。
- 通常の生成で、任意の発言だけを意図的に間引く機能は想定していません。
- Sessionファイルの更新では、今回生成しなかった既存の要約も保持します。
- 本文が変わった場合の判定には、Tool／CoTを除いた会話本文のハッシュを使う案です。
- 明示的な再生成のCLI、失敗した発言の扱い、保存のタイミングは後述の未確定事項です。

## 3. 要約providerと本家設定の参照

### 3.1 合意事項

**Entireと同じprovider設定を使って、timeline独自の要約を生成します。** 本家のCheckpoint全体の要約を、そのまま発言要約として利用する方式ではありません。

- `summary_generation.provider`と`summary_generation.model`に従います。
- 初期対応providerは`copilot-cli`のみです。Copilot CLIの呼び出し方は、Entire本体の実装を参考にします。
- Copilot CLIの呼び出しはtimeline側で実装します。Claude・Codex等の呼び出しAdapterは今回追加しません。
- EntireのGoパッケージに依存するとしても、まずは設定パーサー等に限定して検討します。生成処理全体への直接依存は前提にしません。
- 元会話を記録したAgentと、要約を生成するproviderは独立に扱います。

### 3.2 設定ファイルの方針

**今回のMVPではtimeline専用設定ファイルを導入しません。**

- 本家の`.entire/settings.json`と`.entire/settings.local.json`等から、有効な設定を読み取ります。
- 本家の設定の優先順位と上書き規則に合わせます。provider変更時のmodelの扱いも現行実装で確認します。
- 本家の設定ファイルをtimeline側で書き換えたり、timeline専用キーを追記したりしません。
- 将来timeline専用設定を導入する場合は、本家とは別ファイルにします。その設計は今回の対象外です。

パーサーの流用と独自の限定的な読み取りのどちらを使うかは、依存関係、Goの要求バージョン、設定規則への追従を確認して決めます。

本家で`copilot-cli`以外のproviderが設定されている場合は、今回の要約機能では未対応であることを明示してエラーにします。設定を無視してCopilot CLIへ切り替えません。Copilot CLIが利用不能の場合の診断と、provider未設定時の選択方法は実装前に確定します。

## 4. ファイル構造とレコード

### 4.1 合意したファイル分割

timeline専用メタデータブランチのtree内を、次のように分けます。

```text
summaries/
└── copilot-cli/
    ├── <session-id-A>.jsonl
    └── <session-id-B>.jsonl
```

- Agentごとにディレクトリを分けます。
- Sessionごとに1ファイルとし、JSONLの1行を1発言の要約にします。
- ファイル名は元のSession IDを使い、人間が追えるようにします。
- Session IDのハッシュ化や、先頭・末尾文字によるディレクトリ分散は行いません。
- IDにパス区切り等が含まれる場合は、元に戻せるエスケープを使います。異なるIDが同じファイル名になる変換は避けます。
- ファイルを単独で扱えるように、レコードにもAgentとSession IDを残します。

保存先の計算は一箇所にまとめます。将来ファイル数が増えた場合は、レコード内の識別情報を使って保存構造をマイグレーションできるようにします。

### 4.2 レコードのたたき台

以下は議論中に出たデータ構造案です。フィールド名の最終決定は実装前に行います。実ファイルでは各レコードを1行のJSONにします。

```json
{
  "schema_version": 1,
  "session_id": "session-xxx",
  "agent": "copilot-cli",
  "source_line": 42,
  "event_id": "original-event-id",
  "text_sha256": "sha256-of-conversation-text",
  "summary": "既存のAuthProviderを使った認証機能の実装を依頼。",
  "generation": {
    "provider": "copilot-cli",
    "model": "configured-model",
    "prompt_version": 1
  },
  "created_at": "2026-10-22T10:00:00Z"
}
```

最低限、以下の情報を持つ方針です。

- スキーマのバージョン。
- 元のAgent・Session IDと、発言を照合する情報。
- 要約本文。
- 要約生成に使ったprovider・model・プロンプトの版を追跡する情報。

発言の照合は、当面`agent + session_id + source_line`と本文ハッシュを使う案です。元イベントIDが存在すれば併せて保持します。UserのID欠落を許容し、同じ時刻・本文の別発言を誤って同一視しないようにします。

`source_line`は現在のJSONL出力と同じ、生Transcriptの0始まりの物理行位置です。永続的な発言IDを保証するものではありません。本文ハッシュは誤った要約の流用を防ぐためのもので、ファイル名をハッシュ化する案とは別です。

modelを明示していない場合など、providerが実際に選んだモデルを取得できなければ、その値を推測して保存しません。指定値と実効値の表現、無効化・再生成の条件は詳細設計で決めます。

## 5. ローカル保存先

### 5.1 合意した方式

**作業リポジトリと同じローカルGitのオブジェクト保存領域に、timeline専用の履歴を持ちます。** 別の保存用リポジトリをローカルにcloneして常設する方式ではありません。

ブランチ名は今回固定とします。

```text
entire-timeline/metadata/v1
```

完全なref名は`refs/heads/entire-timeline/metadata/v1`です。

- コードの履歴と独立したメタデータ用の履歴として作ります。
- 通常利用時に作業ブランチをcheckoutし直しません。
- ユーザーのindex・作業ツリーを使わず、専用ブランチのtree・commit・refを読み書きします。
- 要約生成後は、生成結果をこのローカルブランチにcommitして永続化します。
- **ローカルcommitとリモートpushは別操作**です。生成しただけで即pushしません。

最初に検討した作業ツリー直下の`.entire-timeline/summaries/`への常設保存は、今回の採用案ではありません。`summaries/<agent>/<session-id>.jsonl`は専用ブランチ内のパスです。

一時ファイルや排他制御用ファイルが必要な場合の配置は実装時に決めます。worktree利用時は`.git`を固定パスとせず、Gitにgit dir／common dirを解決させます。

### 5.2 元のMVPからの変更範囲

元の[PLAN.md](PLAN.md)で対象外だった要約生成と永続化を、この第2段階で追加します。

本家のTranscript・Checkpoint・設定は引き続き読み取り対象です。新たに書き込むのはtimeline専用のメタデータ履歴と、明示的に登録するtimeline用hookです。

要約はproviderのCLI経由で生成します。独自にLLM APIの認証・HTTP呼び出し基盤を追加することは前提にしません。DBや汎用の永続キャッシュ、タグ、手書きAnnotation、TUI、Web UIは今回の対象外です。

## 6. リモート保存先

### 6.1 本家の設定に従う

**リモートの選択はEntire本体のCheckpoint同期先に従います。** timeline専用の保存先設定は今回追加しません。

- 本家がコードと同じリポジトリへ同期する設定なら、timelineもそこへ同期します。
- 本家が`checkpoint_remote`で別リポジトリへ保存する設定なら、timelineもその保存先へ同期します。
- 明示指定、通常の同期remoteの選択、送信無効化などの有効な設定の解釈は、本家の現行実装で確認します。単純に常に`origin`とする実装は避けます。
- リモートが同じでも、本家のCheckpoint用refとtimeline用ブランチは別に保持します。

本家の別リポジトリ保存は、ローカル保存先を別cloneへ移す方式ではなく、push／fetchに使う送受信先を切り替える方式です。名前付きremoteの追加は必須ではなく、URLを直接使えます。timelineもこの方式に合わせます。

### 6.2 同期アルゴリズム

手動同期コマンドは`entire timeline remote sync`とします。これはtimeline側のコマンド名で、本家のCLIから継承した名前ではありません。本家を参考にするのは、以下の保存・同期フローです。

合意したフローは次のとおりです。

```text
要約生成
  ↓
timeline専用ブランチにローカルcommit
  ↓
同期時にpushを試す
  ↓ 履歴の分岐で拒否された場合
送信先からfetch
  ↓
リモートの先頭にローカルの変更を載せ直す
  ↓
再度push
```

- fetch時に取り込むのは、timeline専用メタデータの履歴です。
- 載せ直すのはローカルcommitで変更したSessionファイルの差分です。ローカルのtree全体でリモートのtreeを置換する方式にはしません。
- ローカルで変更していない別Sessionのファイルは、リモート側の更新を保持します。
- 同じSessionのファイルは、載せ直したローカル側の内容で**ファイル単位の後勝ち上書き**とします。
- 発言レコード単位のマージや、要約本文の内容比較による競合解決は導入しません。
- 後勝ちは生成日時の新旧で選ぶ意味ではありません。今回同期しているローカル側の変更を、取得したリモート履歴の上に適用する意味です。
- 載せ直し時にLLMで要約を再生成せず、生成済みの内容を使います。
- リモートのcommitを祖先として保持し、履歴分岐をforce pushで解決する方式にはしません。

「既存の要約A・Bを保持し、未要約のCを埋めてファイルを書き戻す」動作を基本とします。発言ごとに複雑な競合解決基盤を作るより、Session単位の保存と本家と同様の同期フローを優先します。

初回push、初回の取得、認証・通信エラー、再試行回数、同時実行の排他制御等の細部は実装時に決めます。

## 7. Git hookとの連携

### 7.1 調査で確認した本家の仕組み

- EntireはGitの標準hookを利用しています。
- 本家のGit hook登録は`entire enable`等の設定処理で行われ、再インストールには`entire configure --force`があります。
- 本家には既存hookを`.pre-entire`へ退避し、それも呼び出す仕組みがあります。
- ただし、外部コマンドプラグインを本家の保存・push処理に登録する拡張APIはありません。
- 本家の外部コマンドresolverが、timelineのGit hookを自動登録することもありません。

### 7.2 timeline側で持つ機能

timeline側にhookの登録・解除・状態確認を行うサブコマンドを追加する案です。同期処理はhookから独立したコマンドとしても実行できるようにします。

- `pre-push`から、保存済み要約の同期を起動する構成を想定します。
- hook内で新たな要約生成を始めず、モデルの応答待ちを通常のpushに追加しません。
- 既存のEntire hookやユーザーのhookを保持して共存させます。
- インストールの再実行で二重登録せず、解除時はtimelineが追加した部分だけを除きます。
- hook内部のメタデータpushが再び同じ同期を起動することを防ぎます。
- Entireのhook再インストール後の扱い、`core.hooksPath`や既存hook管理ツールとの共存は、登録方法を決める際に確認します。

以前の議論では、競合時は要約同期を保留して通常のコードpushを続行する案が出ました。その後、同一Sessionファイルの競合は後勝ちで自動処理する方針になっています。認証・通信等で同期を完了できない場合のhook終了方針は、未確定事項として残します。

## 8. CLIインターフェース案

手動同期のコマンド名は`entire timeline remote sync`とします。リモート操作であることを`remote`配下で明示します。生成とhook管理については、以下のコマンド構成をたたき台とします。**生成・hook管理の名前、引数、表示オプションは未確定です。以下の追加コマンドはすべて未実装で、現時点では実行できません。** 通常の表示コマンドは既存どおり維持します。

```sh
# 指定範囲に対応する発言の要約生成・ローカル保存（仮）
entire timeline summarize main...HEAD

# メタデータのリモート同期
entire timeline remote sync

# 自動同期用hookの管理（案）
entire timeline hooks install
entire timeline hooks uninstall
entire timeline hooks status
```

直接実行の`entire-timeline`でも同等の機能を使えるようにします。診断と進捗はstderrに分離し、JSONLのstdoutを壊さない方針を維持します。

## 9. 実装前に確定する事項

以下は、この議論だけでは最終決定していません。

1. **生成・表示のCLI**：生成サブコマンド名、要約の表示方法、本文との併記、JSONLへの追加フィールド。同期コマンド名は`entire timeline remote sync`で確定しています。
2. **生成対象の範囲**：指定コミット範囲の発言を生成するか、そこに含まれるSession全体の未要約発言を埋めるか。既存の範囲外要約はどちらでも保持します。
3. **生成時の文脈**：各発言だけを渡すか、前後の会話も文脈として渡すか。要約の単位は発言ごとで固定です。
4. **Assistantの中間応答**：自然言語の中間メッセージを含める範囲、ストリーミング断片を発言としてまとめる方法。
5. **Copilot生成の詳細**：provider未設定時の挙動、タイムアウト、モデル既定値、パーサーへの依存方法。初期対応providerは`copilot-cli`で確定しています。
6. **レコードの詳細**：発言の照合キー、本文ハッシュの計算規則、再生成指定、プロンプト変更時の既存要約の扱い。
7. **部分失敗と保存**：生成途中の失敗時にどこまで保存するか、Session内・Session間の実行順序や並行度。
8. **同期の起動・終了方針**：初回利用時の取得、送信無効化設定と明示的な`remote sync`の関係、失敗時のhookの終了コードと再試行。
9. **hookの共存方法**：既存hookへの登録手順、Entireの再インストールや既存管理ツールとの共存。

これらを未合意のまま「決定事項」として固定しないでください。単純な実装で済むものは、合意済みの方針に沿って具体化します。

## 10. 検証項目

既存の復元・範囲抽出・全文表示・JSONL出力のテストを維持し、追加機能はFixture、providerのFake、ローカルbareリポジトリを使って検証する計画です。

- CopilotのネイティブJSONLとEntire compact v1形式を初期の要約対象とする。
- User／Assistantのみを要約し、Toolのみ・CoTのみのイベントを生成対象にしない。
- 混在イベントから会話本文だけを入力にし、複数content blockと複数行本文を保持する。
- 元イベント、Tool、CoTは既存の復元結果・JSONLから失われない。
- 既存の要約A・Bを保持して未要約のCを生成・保存できる。
- IDのないUser、同じ時刻・本文の別発言を正しく照合できる。
- 本家設定の上書き規則、provider／model、通常・別リポジトリの同期先を解決できる。
- 本家でCopilot以外のproviderが設定されている場合に、無言で切り替えず未対応エラーを返す。
- Copilot以外の会話データを要約しようとした場合に、対象外であることを明示する。
- 専用設定ファイルや、本家設定へのキー追加・書き換えを行わない。
- Agent別・Session別の保存と、Session IDの可逆なファイル名変換を検証する。
- 要約生成後、リモートへ送信せずにローカルGitから要約を読み返せる。
- 保存・同期で作業ブランチ、index、作業ツリー、本家Checkpointの内容を変更しない。
- 通常push、初回push、履歴分岐後のfetch・載せ直し・再pushを検証する。
- 別Sessionのリモート更新を保持し、同じSessionはローカルのファイル内容が後勝ちになる。
- 載せ直しが発言単位のマージや要約の再生成を行わず、リモート履歴も祖先として保持する。
- 別リポジトリへの同期でも、別のローカルcloneや名前付きremoteの追加を必須にしない。
- hookの登録・再登録・解除・状態確認、既存hookとの共存、同期の再帰防止を検証する。
- 未確定事項の決定に合わせ、生成失敗・認証失敗・通信失敗・キャンセル時の挙動を検証する。

## 11. 実装の進め方

1. 未確定事項を整理し、Entireの現行ソースとインストール済みCLIの仕様を再確認する。
2. 会話本文・Tool・CoTを区別する解析と、要約対象の抽出を実装する。
3. 本家設定の読み取りと、Copilot CLIによる要約生成の呼び出しを実装する。
4. 発言ごとの要約生成、Session別レコード、専用ローカルブランチへの保存を実装する。
5. 既存表示・JSONL出力への要約の反映と、生成用CLIを実装する。
6. `entire timeline remote sync`を実装し、通常pushと履歴分岐後の復旧を検証する。
7. 既存hookとの共存方法を確定して、自動同期用hookの管理機能を追加する。
8. README、設定の参照範囲、要約生成・同期の実行例、失敗時の挙動を記載する。

機能とテストを意味のある単位でコミットしながら進めます。

## 12. 本家調査の参照先

議論では、2026-10-22に取得したEntire公式ソースのコミット[`29f996951b44aeb0b9e834c2de2337eba65fd324`](https://github.com/entireio/cli/tree/29f996951b44aeb0b9e834c2de2337eba65fd324)まで確認しました。実装依存部分は実装着手時に再確認します。

- [Session・Checkpointの保存構造](https://github.com/entireio/cli/blob/29f996951b44aeb0b9e834c2de2337eba65fd324/docs/architecture/sessions-and-checkpoints.md)
- [git-refsの保存とpush](https://github.com/entireio/cli/blob/29f996951b44aeb0b9e834c2de2337eba65fd324/docs/architecture/ref-checkpoint-backend.md)
- [外部コマンドと設定・hookの境界](https://github.com/entireio/cli/blob/29f996951b44aeb0b9e834c2de2337eba65fd324/docs/architecture/external-commands.md)
- [設定とsummary_generation](https://github.com/entireio/cli/blob/29f996951b44aeb0b9e834c2de2337eba65fd324/cmd/entire/cli/settings/settings.go)
- [要約providerの解決](https://github.com/entireio/cli/blob/29f996951b44aeb0b9e834c2de2337eba65fd324/cmd/entire/cli/explain_summary_provider.go)
- [Checkpointの要約生成・保存](https://github.com/entireio/cli/blob/29f996951b44aeb0b9e834c2de2337eba65fd324/cmd/entire/cli/explain.go)
- [リモートの選択](https://github.com/entireio/cli/blob/29f996951b44aeb0b9e834c2de2337eba65fd324/cmd/entire/cli/strategy/checkpoint_remote.go)
- [push失敗後のfetch・再適用](https://github.com/entireio/cli/blob/29f996951b44aeb0b9e834c2de2337eba65fd324/cmd/entire/cli/strategy/push_common.go)
- [ローカルcommitの変更再適用](https://github.com/entireio/cli/blob/29f996951b44aeb0b9e834c2de2337eba65fd324/cmd/entire/cli/strategy/metadata_reconcile.go)
- [ファイル単位のtree更新](https://github.com/entireio/cli/blob/29f996951b44aeb0b9e834c2de2337eba65fd324/cmd/entire/cli/checkpoint/parse_tree.go)
- [Git hook登録・既存hookの連結](https://github.com/entireio/cli/blob/29f996951b44aeb0b9e834c2de2337eba65fd324/cmd/entire/cli/strategy/hooks.go)

push復旧の説明コメントには「重なる変更は競合として保留」とありますが、調査時の`cherryPickOnto`→`ApplyTreeChanges`では、変更したパスにローカル側のファイルを適用していました。本書のSessionファイル単位の後勝ちは、この読み取りを踏まえて議論で明示的に採用したtimelineの仕様です。本家の説明だけを根拠に、発言単位のマージがあるものとは扱いません。
