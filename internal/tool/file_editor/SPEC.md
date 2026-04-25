# file_editor SPEC

## 1. Scope

本文件是 `internal/tool/file_editor` 的规范性约束。

本文件中的规则全部为可测试规则。

本文件中的关键词含义如下：

- `必须` 表示实现不满足该条即为不符合规范。
- `禁止` 表示实现出现该行为即为不符合规范。
- `成功` 指返回 `ErrorStatus=false` 的 `FileEditorObservation`。
- `失败` 指返回 `ErrorStatus=true` 的 `FileEditorObservation`。

## 2. Non-Goals

以下能力不属于本模块范围：

- 不支持多文件事务。
- 不支持跨文件原子提交。
- 不支持 Git 级别版本管理。
- 不支持自由格式 patch 语言。
- 不支持二进制文件编辑。
- 不支持隐藏文件自动展开浏览。
- 不支持无限历史记录。
- 不支持跨进程并发协调。

## 3. Invariants

以下规则对所有 command 生效：

- `path` 必须是绝对路径。
- `path` 在执行前必须经过 `filepath.Clean` 语义的规范化。
- 所有命令必须先执行参数校验，再执行文件系统操作。
- 所有命令必须返回 `FileEditorObservation`，禁止返回其他结果类型。
- 任何失败路径都必须返回 error observation，禁止 panic。
- `ErrorStatus=false` 与“文件系统状态已满足命令成功行为”必须同时成立。
- `ErrorStatus=true` 与“命令成功行为未完全达成”必须同时成立。
- 成功 observation 中 `Command` 必须等于输入 command。
- 成功 observation 中 `Path` 必须等于规范化后的绝对路径。
- `view` 以外的编辑命令在修改已有文件内容前，必须先把旧内容追加到 `historyManager` 维护的版本链。
- `historyManager` 为 `nil` 时，任何依赖历史记录的编辑命令必须失败。
- 所有输入行号必须按 1-based 语义解释。
- 目录深度上限固定为 2。
- 文本查看大小上限固定为 `5MB`。
- 图片查看大小上限固定为 `10MB`。
- 查看输出截断上限固定为 `128KB`。

## 4. Error Semantics

### 4.1 Error Observation

- 失败结果必须通过 `NewErrorObservation` 构造，或与其结构完全等价。
- 失败结果的 `ErrorStatus` 必须为 `true`。
- 失败结果的 `Content` 必须至少包含一个文本消息。
- 失败结果的 `Command` 必须等于输入 command。
- 输入 `path` 非空时，失败结果的 `Path` 必须存在。

### 4.2 Error Categories

- 缺少必填参数时，错误类型必须可归类为 `EditorToolParameterMissingError`。
- 参数值非法时，错误类型必须可归类为 `EditorToolParameterInvalidError`。
- 文件路径、文件状态、文件内容状态非法时，错误类型必须可归类为 `FileValidationError`。
- 不支持的命令必须返回失败，不得降级为其他命令。

### 4.3 No Partial Success

- 如果命令创建了一个原本不存在的文件，且该命令最终返回失败，则该文件在返回前必须被删除。
- `create` 在写入版本链失败时，必须删除刚创建的文件。
- 失败结果禁止标记为 `ErrorStatus=false`。

## 5. Command Contract

### 5.1 `view`

#### 输入合法条件

- `command` 必须等于 `view`。
- `path` 必须非空且为绝对路径。
- `view_range` 只能为空，或恰好包含两个整数。
- `view_range` 非空时，两个行号都必须大于等于 1。
- `view_range` 非空时，起始行号必须小于等于结束行号。

#### 成功行为

- 当 `path` 指向文本文件时，返回该文件内容。
- 当 `path` 指向目录时，返回目录内容列表。
- 当 `path` 指向受支持图片文件时，返回文本说明和图像内容。
- 文本文件查看结果必须带行号。
- `view_range` 非空时，返回结果只包含指定区间内的行。
- 目录查看结果最多展开两层深度。
- 目录查看结果必须过滤隐藏文件和隐藏目录。
- 当目录中存在被过滤的隐藏项时，输出文本必须包含被过滤数量。

#### 失败行为

- `path` 不存在时必须失败。
- 文本文件大小超过 `5MB` 时必须失败。
- 图片文件大小超过 `10MB` 时必须失败。
- 文件内容为二进制且不是受支持图片时必须失败。
- `view_range` 起始行超出文件总行数时必须失败。
- 空文件配合非空 `view_range` 时必须失败。
- 图片后缀与实际内容不匹配时必须失败。

#### 边界条件

- 空文本文件在 `view_range` 为空时必须成功，并返回空文件表示。
- `view_range` 结束行超过文件总行数时，结果必须截断到文件最后一行。
- 输出内容超过 `128KB` 时，结果必须截断并追加 `...[truncated]`。
- 目录查看顺序必须按名称升序排序。

### 5.2 `create`

#### 输入合法条件

- `command` 必须等于 `create`。
- `path` 必须非空且为绝对路径。
- `file_text` 必须存在。
- `file_text` 字节长度必须小于等于 `MAX_FILE_SIZE_MB` 对应的上限。

#### 成功行为

- 当目标文件不存在且父目录存在时，必须创建目标文件。
- 新文件内容必须与 `file_text` 完全一致。
- 成功后 `PrevExist` 必须为 `false`。
- 成功后 `NewContent` 必须等于 `file_text`。
- 成功后必须向版本链写入一条历史记录。
- 该历史记录的旧内容必须为空字符串。
- 成功后返回 success observation。

#### 失败行为

- 目标路径已存在且是普通文件时必须失败。
- 目标路径已存在且是目录时必须失败。
- 父目录不存在时必须失败。
- 父路径存在但不是目录时必须失败。
- 创建文件失败时必须失败。
- 写入文件失败时必须失败。
- 写入版本链失败时必须失败并删除刚创建的文件。

#### 边界条件

- `file_text` 允许为空字符串。
- 空字符串文件创建成功后，磁盘文件大小必须为 `0`。
- `file_text` 字节长度大于上限时必须失败，且不得创建文件。

### 5.3 `str_replace`

#### 输入合法条件

- `command` 必须等于 `str_replace`。
- `path` 必须非空且为绝对路径。
- `old_str` 必须存在且不能为空字符串。
- `new_str` 必须存在。

#### 成功行为

- 目标文件必须被当作文本文件读取。
- 实现必须先将 `old_str` 作为正则表达式匹配文件内容。
- 首次匹配次数为 `0` 时，实现必须对 `old_str` 和 `new_str` 执行首尾空白去除，并仅重试一次。
- 最终匹配次数必须恰好为 `1`。
- 替换前必须将完整旧内容写入版本链。
- 替换后磁盘文件内容必须等于唯一替换后的结果。
- 成功 observation 中 `OldContent` 必须等于替换前内容。
- 成功 observation 中 `NewContent` 必须等于替换后内容。
- 成功 observation 的 `PrevExist` 必须为 `true`。
- 成功 observation 文本必须包含受影响行预览。
- 成功 observation 文本必须包含“检查一下看是否符合预期，否则可以重新编辑”。

#### 失败行为

- 目标文件不存在时必须失败。
- 目标文件不是文本文件时必须失败。
- 最终匹配次数为 `0` 时必须失败，且错误文本必须包含“没有进行替换，因为没有找到旧字符串”。
- 最终匹配次数大于 `1` 时必须失败，且错误文本必须包含“没有进行替换，因为找到多处旧字符串”。
- `old_str` 不是合法正则表达式时必须失败。
- 写入版本链失败时必须失败，且不得修改磁盘文件。
- 写回文件失败时必须失败。

#### 边界条件

- `new_str` 允许为空字符串。
- `old_str` 位于文件首部时必须可替换。
- `old_str` 位于文件尾部时必须可替换。
- 文件为空时必须失败。
- 当首次匹配失败且去除首尾空白后匹配成功时，替换逻辑必须使用去除首尾空白后的 `old_str` 和 `new_str`。

### 5.4 `insert`

#### 输入合法条件

- `command` 必须等于 `insert`。
- `path` 必须非空且为绝对路径。
- `insert_line` 必须存在。
- `insert_line` 必须大于等于 `1`。
- `new_str` 与 `file_text` 至少一个存在。
- 当 `new_str` 与 `file_text` 同时存在时，插入文本必须取 `new_str`。

#### 成功行为

- 目标文件必须被当作文本文件读取。
- 插入前必须将完整旧内容写入版本链。
- 插入后磁盘文件内容必须等于按行插入后的结果。
- 成功 observation 中 `OldContent` 必须等于插入前内容。
- 成功 observation 中 `NewContent` 必须等于插入后内容。
- 成功 observation 的 `PrevExist` 必须为 `true`。

#### 失败行为

- 目标文件不存在时必须失败。
- 目标文件不是文本文件时必须失败。
- `insert_line` 超出允许范围时必须失败。
- 写入版本链失败时必须失败，且不得修改磁盘文件。
- 写回文件失败时必须失败。

#### 边界条件

- 空文件只允许在第 `1` 行插入。
- 非空文件允许在最后一行后方插入一段新内容。
- 当实际插入文本为空字符串时，命令必须成功，且写回后的文件内容必须与插入前完全一致。

### 5.5 `undo_edit`

#### 输入合法条件

- `command` 必须等于 `undo_edit`。
- `path` 必须非空且为绝对路径。

#### 成功行为

- 必须从 `historyManager` 读取最近一次历史快照。
- 必须将目标文件恢复为该快照内容。
- 成功 observation 中 `OldContent` 必须等于撤销前内容。
- 成功 observation 中 `NewContent` 必须等于撤销后内容。
- 成功后该历史快照必须被消费一次。

#### 失败行为

- 没有可撤销历史时必须失败。
- 当目标文件在执行开始时不存在时，命令必须失败。
- 历史记录损坏时必须失败。
- 写回文件失败时必须失败。

#### 边界条件

- 连续执行多次 `undo_edit` 时，必须按后进先出顺序回滚。
- 撤销次数超过历史条数时，超出的那次必须失败。

## 6. History Contract

以下规则适用于 `fileHistoryManager`：

- `add(filePath, content)` 必须在 `filePath` 维度追加一个新版本。
- `pop(filePath)` 必须返回最近一次 `add` 的内容。
- `pop(filePath)` 必须消费该最近版本。
- `maxHistoryPerFile > 0` 时，超出上限的最旧版本必须被删除。
- 对不存在历史的 `pop(filePath)` 必须返回 `("", false, nil)`。
- 当 metadata 指向的版本内容缺失时，`pop(filePath)` 必须删除该悬空版本；若删除后该文件没有剩余历史，则必须删除该文件对应的 metadata。

## 7. Cache Contract

以下规则适用于 `fileCache`：

- `set(key, value)` 必须将内容落盘到缓存目录。
- 缓存文件名必须由 `key` 的 SHA-256 哈希生成。
- 缓存文件内容必须是 JSON。
- `get(key)` 命中时必须返回原始 `value`。
- `delete(key)` 对不存在 key 必须是 no-op。
- `clear()` 执行后缓存目录必须为空。
- 配置了 `sizeLimit` 时，超限写入前必须执行 LRU 淘汰。
- LRU 依据必须是缓存文件的最近访问/修改时间。
- 写入必须使用原子写入语义。

## 8. Forbidden Behaviors

- 禁止对相对路径执行文件系统操作。
- 禁止对二进制文件执行文本编辑。
- 禁止在 `str_replace` 中执行多匹配替换。
- 禁止在失败后返回 success observation。
- 禁止用 panic 表达可预期错误。
- 禁止在未写入 undo log 的情况下声称编辑成功。
- 禁止隐式修改 `command` 字段语义。
- 禁止依赖当前工作目录解释相对路径。
