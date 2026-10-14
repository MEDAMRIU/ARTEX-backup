"use client";

import * as React from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";

type Account = { id: string; email: string; client_id: string; connected: boolean; plan_enabled: boolean };

export function ChatGPTAccounts({ value, onChange }: { value: string; onChange: (id: string) => void }) {
  const [accounts, setAccounts] = React.useState<Account[]>([]);
  const [attempt, setAttempt] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [message, setMessage] = React.useState("");
  const [loginURL, setLoginURL] = React.useState("");
  const reload = React.useCallback(async () => {
    const r = await api.chatGPTAccounts();
    setAccounts(r.accounts);
  }, []);
  React.useEffect(() => { void reload().catch(() => setMessage("Could not load ChatGPT accounts.")); }, [reload]);
  React.useEffect(() => {
    if (!attempt) return;
    let cancelled = false;
    const timer = setInterval(async () => {
      try {
        const result = await api.chatGPTSignInStatus(attempt);
        if (cancelled || result.status === "pending" || result.status === "exchanging") return;
        clearInterval(timer); setAttempt(""); setLoginURL("");
        if (result.status === "complete") {
          await reload();
          if (!cancelled) { onChange(result.account_id); setMessage("Connected. Load your available models, choose one, then save this profile."); }
        } else setMessage(result.error || "Sign-in did not complete. Try again.");
      } catch { if (!cancelled) setMessage("Waiting for ARTEX. Keep this settings window open."); }
    }, 1500);
    return () => { cancelled = true; clearInterval(timer); };
  }, [attempt, onChange, reload]);

  async function signIn(id = "") {
    if (busy || attempt) return;
    // Open synchronously from the user's click so popup blocking does not lose the flow.
    const popup = window.open("about:blank", "_blank");
    if (popup) popup.opener = null;
    setBusy(true); setMessage("");
    try {
      const r = await api.chatGPTSignIn(id);
      setAttempt(r.id); setLoginURL(r.url);
      if (popup) popup.location.replace(r.url);
      setMessage("Finish signing in with OpenAI, then return here. This link expires in five minutes.");
    } catch (e) { popup?.close(); setMessage((e as Error).message); }
    finally { setBusy(false); }
  }
  async function signOut() {
    setBusy(true);
    try {
      const r = await api.chatGPTSignOut(value);
      await reload();
      setMessage(r.revocation_confirmed ? "Signed out of this ChatGPT connection." : "Signed out locally. Remote revocation was not confirmed; disconnect ARTEX in ChatGPT Settings → Usage.");
    } catch (e) { toast.error((e as Error).message); }
    finally { setBusy(false); }
  }
  const selected = accounts.find((a) => a.id === value);
  return <section className="grid gap-3 rounded-lg border p-3" aria-label="ChatGPT connection">
    <label className="text-sm font-medium" htmlFor="chatgpt-account">Authentication</label>
    <select id="chatgpt-account" className="bg-background rounded-md border p-2 text-sm" value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">Provider API key</option>
      {accounts.map((a) => <option key={a.id} value={a.id}>{a.email || "ChatGPT account"} · {a.client_id.slice(-8)}{a.connected ? "" : " · signed out"}</option>)}
    </select>
    <div className="flex flex-wrap gap-2">
      <Button type="button" variant="outline" disabled={busy || !!attempt} onClick={() => void signIn(value)}>Continue with ChatGPT</Button>
      {value && <Button type="button" variant="ghost" disabled={busy || !!attempt} onClick={() => void signIn()}>Add another account</Button>}
      {selected?.connected && <Button type="button" variant="ghost" disabled={busy || !!attempt} onClick={() => void signOut()}>Sign out</Button>}
      <a className="self-center text-sm underline" href="https://chatgpt.com/settings/usage" target="_blank" rel="noreferrer">Manage usage</a>
    </div>
    {value && <p className="text-sm">{selected?.connected && selected.plan_enabled ? "Using ChatGPT plan" : "ChatGPT plan usage is unavailable. Reconnect or enable access in ChatGPT settings."}</p>}
    <p className="text-muted-foreground text-xs">Connect your ChatGPT plan on the computer running ARTEX. Tokens stay on the server. Your plan limits apply; this connection does not fall back to an API key.</p>
    {message && <p role="status" className="text-sm">{message}</p>}
    {loginURL && <a href={loginURL} target="_blank" rel="noreferrer" className="text-sm underline">Open ChatGPT sign-in</a>}
  </section>;
}
