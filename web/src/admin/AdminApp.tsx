import { Fragment, useCallback, useState } from "react";
import type { FormEvent } from "react";
import { QueryClient, QueryClientProvider, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, LogOut, ShieldCheck } from "lucide-react";
import { adminRequest, AdminAPIError } from "./api";
import type { AdminUserPage } from "./api";
import type { HistoryPageDTO, ProjectListDTO, SessionDTO, SessionPageDTO } from "../types/api";
import { historyToMessage } from "../types/chat";
import { sessionLabel } from "../components/session-tabs/label";
import { MessageItem } from "../components/message-item/MessageItem";
import { ToolMessageBlock } from "../components/tool-message-block/ToolMessageBlock";
import { groupMessages } from "../components/message-list/group";
import "./admin.css";

const storageKey = "laxcode.admin.token";
const dateLabel = (value: string) => new Date(value).toLocaleString("zh-CN", { hour12: false });
type Read = <T>(path: string, signal?: AbortSignal) => Promise<T>;
type Selection = { userID: string; projectID?: string; session?: SessionDTO };

export function AdminApp() {
  const [token, setToken] = useState<string | undefined>(() => {
    try { return sessionStorage.getItem(storageKey) || undefined; } catch { return undefined; }
  });
  const saveToken = useCallback((value?: string) => {
    try { if (value) sessionStorage.setItem(storageKey, value); else sessionStorage.removeItem(storageKey); } catch { /* Login still works for this page when storage is disabled. */ }
    setToken(value);
  }, []);
  return token ? <AdminSession key={token} token={token} onLogout={() => saveToken()} /> : <Login onLogin={saveToken} />;
}

function Login({ onLogin }: { onLogin: (token: string) => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setPending(true); setError("");
    try {
      const result = await adminRequest<{ token: string }>("/login", undefined, { method: "POST", body: JSON.stringify({ username, password }) });
      setPassword(""); onLogin(result.token);
    } catch (error) { setError(error instanceof Error ? error.message : "登录失败"); }
    finally { setPending(false); }
  };
  return <main className="admin-login"><form onSubmit={(event) => void submit(event)}>
    <ShieldCheck size={30} /><h1>LaxCode 管理后台</h1><p>使用管理员账号登录</p>
    <label>管理员账号<input name="username" autoComplete="username" required maxLength={128} value={username} onChange={(event) => setUsername(event.target.value)} /></label>
    <label>密码<input name="password" type="password" autoComplete="current-password" required maxLength={1024} value={password} onChange={(event) => setPassword(event.target.value)} /></label>
    {error && <p className="admin-error" role="alert">{error}</p>}
    <button type="submit" disabled={pending}>{pending ? "正在登录…" : "登录"}</button><a href="/">返回 LaxCode</a>
  </form></main>;
}

function AdminSession({ token, onLogout }: { token: string; onLogout: () => void }) {
  // A fresh cache per login prevents one admin session's results surviving logout.
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 10000, refetchOnWindowFocus: false } } }));
  const read: Read = useCallback(async <T,>(path: string, signal?: AbortSignal) => {
    try { return await adminRequest<T>(path, token, { signal }); }
    catch (error) { if (error instanceof AdminAPIError && error.status === 401) onLogout(); throw error; }
  }, [token, onLogout]);
  return <QueryClientProvider client={client}><Authenticated read={read} onLogout={onLogout} /></QueryClientProvider>;
}

function Authenticated({ read, onLogout }: { read: Read; onLogout: () => void }) {
  const me = useQuery({ queryKey: ["me"], queryFn: ({ signal }) => read<{ username: string }>("/me", signal) });
  if (me.isPending) return <main className="admin-status" role="status">正在验证登录…</main>;
  if (me.isError) return <main className="admin-status"><Failure error={me.error} retry={() => void me.refetch()} /><button onClick={onLogout}>重新登录</button></main>;
  return <Dashboard read={read} username={me.data.username} onLogout={onLogout} />;
}

function Dashboard({ read, username, onLogout }: { read: Read; username: string; onLogout: () => void }) {
  const [selection, setSelection] = useState<Selection>();
  const users = useInfiniteQuery({
    queryKey: ["users"], initialPageParam: "",
    queryFn: ({ pageParam, signal }) => read<AdminUserPage>(`/users?${new URLSearchParams({ limit: "100", ...(pageParam ? { after_user_id: pageParam } : {}) })}`, signal),
    getNextPageParam: (page) => page.has_more ? page.next_after_user_id : undefined,
  });
  return <div className="admin-shell">
    <header className="admin-header"><div><ShieldCheck size={21} /><strong>LaxCode 管理后台</strong><span>聊天记录</span></div><div><span>{username}</span><a href="/">前台首页</a><button onClick={onLogout}><LogOut size={15} />退出</button></div></header>
    <div className="admin-body"><aside className="admin-sidebar" aria-label="用户、项目和会话">
      <div className="admin-sidebar-heading"><strong>用户</strong><button disabled={users.isFetching} onClick={() => void users.refetch()}>刷新</button></div>
      {users.isPending && <p role="status">正在加载用户…</p>}
      {users.isError && <Failure error={users.error} retry={() => void users.refetch()} />}
      {users.data?.pages[0]?.users.length === 0 && <p>暂无用户</p>}
      {users.data?.pages.flatMap((page) => page.users).map((user) => <div className="admin-user" key={user.user_id}>
        <button className="admin-tree-button" aria-expanded={selection?.userID === user.user_id} onClick={() => setSelection(selection?.userID === user.user_id ? undefined : { userID: user.user_id })}>
          {selection?.userID === user.user_id ? <ChevronDown size={16} /> : <ChevronRight size={16} />}<span title={user.user_id}>{user.user_id}</span>
        </button>
        {selection?.userID === user.user_id && <Projects read={read} selection={selection} onSelect={setSelection} />}
      </div>)}
      {users.hasNextPage && <button className="admin-load" disabled={users.isFetchingNextPage} onClick={() => void users.fetchNextPage()}>加载更多用户</button>}
    </aside><section className="admin-content" aria-label="历史消息">
      {selection?.session ? <History key={`${selection.userID}/${selection.session.session_id}`} read={read} userID={selection.userID} session={selection.session} /> : <div className="admin-empty"><ShieldCheck size={32} /><h2>查看历史消息</h2><p>从左侧依次选择用户、项目和会话</p></div>}
    </section></div>
  </div>;
}

function Projects({ read, selection, onSelect }: { read: Read; selection: Selection; onSelect: (value: Selection) => void }) {
  const projects = useQuery({ queryKey: ["projects", selection.userID], queryFn: ({ signal }) => read<ProjectListDTO>(`/users/${selection.userID}/projects`, signal) });
  return <div className="admin-projects">
    {projects.isPending && <p role="status">正在加载项目…</p>}
    {projects.isError && <Failure error={projects.error} retry={() => void projects.refetch()} />}
    {projects.data?.projects.length === 0 && <p>暂无项目</p>}
    {projects.data?.projects.map((project) => <div key={project.project_id}>
      <button className="admin-tree-button" aria-expanded={selection.projectID === project.project_id} onClick={() => onSelect({ userID: selection.userID, projectID: selection.projectID === project.project_id ? undefined : project.project_id })}>
        {selection.projectID === project.project_id ? <ChevronDown size={15} /> : <ChevronRight size={15} />}<span>{project.name}</span>
      </button>
      {selection.projectID === project.project_id && <Sessions read={read} selection={selection} onSelect={onSelect} />}
    </div>)}
  </div>;
}

function Sessions({ read, selection, onSelect }: { read: Read; selection: Selection; onSelect: (value: Selection) => void }) {
  const sessions = useInfiniteQuery({
    queryKey: ["sessions", selection.userID, selection.projectID], initialPageParam: "",
    queryFn: ({ pageParam, signal }) => read<SessionPageDTO>(`/users/${selection.userID}/sessions?${new URLSearchParams({ project_id: selection.projectID!, limit: "50", ...(pageParam ? { before_session_id: pageParam } : {}) })}`, signal),
    getNextPageParam: (page) => page.has_more ? page.next_before_session_id : undefined,
  });
  const items = sessions.data?.pages.flatMap((page) => page.sessions) ?? [];
  return <div className="admin-sessions">
    {sessions.isPending && <p role="status">正在加载会话…</p>}
    {sessions.isError && <Failure error={sessions.error} retry={() => void sessions.refetch()} />}
    {sessions.isSuccess && !items.length && <p>暂无会话</p>}
    {items.map((session, index) => <button key={session.session_id} className={`admin-session ${selection.session?.session_id === session.session_id ? "selected" : ""}`} aria-current={selection.session?.session_id === session.session_id ? "true" : undefined} onClick={() => onSelect({ ...selection, session })}>
      <strong>{sessionLabel(session.title, index, items.length)}</strong><time dateTime={session.created_at}>{dateLabel(session.created_at)}</time>
    </button>)}
    {sessions.hasNextPage && <button className="admin-load" disabled={sessions.isFetchingNextPage} onClick={() => void sessions.fetchNextPage()}>加载更多会话</button>}
  </div>;
}

function History({ read, userID, session }: { read: Read; userID: string; session: SessionDTO }) {
  const history = useInfiniteQuery({
    queryKey: ["history", userID, session.session_id], initialPageParam: 0,
    queryFn: ({ pageParam, signal }) => read<HistoryPageDTO>(`/users/${userID}/sessions/${session.session_id}/messages?${new URLSearchParams({ limit: "50", ...(pageParam ? { before_seq: String(pageParam) } : {}) })}`, signal),
    getNextPageParam: (page) => page.has_more ? page.next_before_seq : undefined,
  });
  const messages = [...(history.data?.pages ?? [])].reverse().flatMap((page) => page.messages).map(historyToMessage);
  return <>
    <header className="admin-history-header"><div><h2>{session.title.trim() || "未命名会话"}</h2><time dateTime={session.created_at}>{dateLabel(session.created_at)}</time></div><button disabled={history.isFetching} onClick={() => void history.refetch()}>刷新消息</button><small>{session.session_id}</small></header>
    <div className="admin-history"><div className="message-column">
      {history.isPending && <p role="status">正在加载历史消息…</p>}
      {history.isError && <Failure error={history.error} retry={() => void history.refetch()} />}
      {history.hasNextPage && <button className="admin-load" disabled={history.isFetchingNextPage} onClick={() => void history.fetchNextPage()}>{history.isFetchingNextPage ? "加载中…" : "查看更早消息"}</button>}
      {history.isSuccess && !messages.length && <p>该会话暂无消息</p>}
      {groupMessages(messages).map((block) => block.kind === "tools" ? <ToolMessageBlock key={block.messages[0].id} messages={block.messages} /> : <Fragment key={block.message.id}>
        <MessageItem message={block.message} userLabel="用户" />
        {!!block.message.upsertFiles?.length && <ul className="admin-files" aria-label="关联文件">{block.message.upsertFiles.map((file) => <li key={file.path}>{file.path}</li>)}</ul>}
      </Fragment>)}
    </div></div>
  </>;
}

function Failure({ error, retry }: { error: Error; retry: () => void }) {
  return <div className="admin-error" role="alert"><p>{error.message}</p><button onClick={retry}>重试</button></div>;
}
