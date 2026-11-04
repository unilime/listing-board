import { FormEvent, useEffect, useState } from "react";

type Step = { label: string; state: string };

type Job = {
  id: string;
  title: string;
  address: string;
  status: string;
  error: string;
  filename: string;
  checklist: Step[];
};

type Account = { id: string; email: string };

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { credentials: "include", ...init });
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) {
    const message = data && typeof data.error === "string" ? data.error : res.statusText;
    throw new Error(message);
  }
  return data as T;
}

export function App() {
  const [me, setMe] = useState<Account | null>(null);
  const [ready, setReady] = useState(false);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [error, setError] = useState("");
  const selected = jobs.find((job) => job.id === selectedId) ?? null;
  const anyRunning = jobs.some((job) => job.status === "running");

  function upsert(job: Job) {
    setJobs((current) =>
      current.some((item) => item.id === job.id)
        ? current.map((item) => (item.id === job.id ? job : item))
        : [job, ...current],
    );
  }

  useEffect(() => {
    api<Account>("/api/me")
      .then(setMe)
      .catch(() => setMe(null))
      .finally(() => setReady(true));
  }, []);

  useEffect(() => {
    if (!me) {
      setJobs([]);
      return;
    }
    api<Job[]>("/api/jobs")
      .then(setJobs)
      .catch((err: Error) => setError(err.message));
  }, [me]);

  // The worker moves a running sheet along on its own, so keep asking the API
  // until nothing is running any more. No reload is needed to see the checklist move.
  useEffect(() => {
    if (!me || !anyRunning) {
      return;
    }
    const timer = setInterval(() => {
      api<Job[]>("/api/jobs")
        .then(setJobs)
        .catch(() => undefined);
    }, 1000);
    return () => clearInterval(timer);
  }, [me, anyRunning]);

  if (!ready) {
    return <p className="muted">Loading…</p>;
  }
  if (!me) {
    return <Auth onIn={setMe} />;
  }

  return (
    <main>
      <header>
        <div>
          <p className="eyebrow">Real estate</p>
          <h1>Listing board</h1>
        </div>
        <button
          className="ghost"
          onClick={() => {
            api("/api/logout", { method: "POST" }).finally(() => {
              setMe(null);
              setSelectedId(null);
            });
          }}
        >
          Log out
        </button>
      </header>
      {error ? <p className="error">{error}</p> : null}
      <Create
        onCreated={(job) => {
          upsert(job);
          setSelectedId(job.id);
          setError("");
        }}
        onError={setError}
      />
      <section className="layout">
        <ul className="list">
          {jobs.length === 0 ? <li className="muted">No sheets yet.</li> : null}
          {jobs.map((job) => (
            <li key={job.id}>
              <button className={selected?.id === job.id ? "row on" : "row"} onClick={() => setSelectedId(job.id)}>
                <span>
                  <strong>{job.title}</strong>
                  <span className="muted">{job.address}</span>
                </span>
                <span className={"status " + job.status}>{job.status}</span>
              </button>
            </li>
          ))}
        </ul>
        {selected ? (
          <Sheet
            job={selected}
            onChange={upsert}
            onError={setError}
          />
        ) : (
          <p className="muted">Select a sheet.</p>
        )}
      </section>
    </main>
  );
}

function Auth({ onIn }: { onIn: (account: Account) => void }) {
  const [mode, setMode] = useState<"login" | "register">("login");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      const account = await api<Account>("/api/" + mode, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, password }),
      });
      onIn(account);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not sign in");
    }
  }

  return (
    <main className="narrow">
      <p className="eyebrow">Real estate</p>
      <h1>Listing board</h1>
      <form onSubmit={submit}>
        <label>
          Email
          <input value={email} onChange={(event) => setEmail(event.target.value)} type="email" required />
        </label>
        <label>
          Password
          <input value={password} onChange={(event) => setPassword(event.target.value)} type="password" required />
        </label>
        {error ? <p className="error">{error}</p> : null}
        <button type="submit">{mode === "login" ? "Sign in" : "Create account"}</button>
      </form>
      <button className="ghost" onClick={() => setMode(mode === "login" ? "register" : "login")}>
        {mode === "login" ? "Need an account?" : "Already have an account?"}
      </button>
    </main>
  );
}

const MAX_BROCHURE = 1 << 20;

function checkSize(file: File): string {
  if (file.size === 0) {
    return "The brochure is empty";
  }
  if (file.size > MAX_BROCHURE) {
    return "The brochure must be at most 1 MiB";
  }
  return "";
}

function upload(id: string, file: File): Promise<Job> {
  const body = new FormData();
  body.append("file", file);
  return api<Job>("/api/jobs/" + id + "/file", { method: "POST", body });
}

function Create({ onCreated, onError }: { onCreated: (job: Job) => void; onError: (message: string) => void }) {
  const [title, setTitle] = useState("");
  const [address, setAddress] = useState("");
  const [file, setFile] = useState<File | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!file) {
      onError("Choose a brochure file");
      return;
    }
    const tooBig = checkSize(file);
    if (tooBig) {
      onError(tooBig);
      return;
    }
    let created: Job | null = null;
    try {
      created = await api<Job>("/api/jobs", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title, address }),
      });
      const saved = await upload(created.id, file);
      setTitle("");
      setAddress("");
      setFile(null);
      onCreated(saved);
    } catch (err) {
      if (created) {
        // The sheet exists even though the brochure was refused. Show it, so
        // the brochure can be attached to it from the sheet.
        setTitle("");
        setAddress("");
        setFile(null);
        onCreated(created);
      }
      onError(err instanceof Error ? err.message : "Could not create the sheet");
    }
  }

  return (
    <form className="create" onSubmit={submit}>
      <label>
        Title
        <input value={title} onChange={(event) => setTitle(event.target.value)} required />
      </label>
      <label>
        Address
        <input value={address} onChange={(event) => setAddress(event.target.value)} required />
      </label>
      <label>
        Brochure
        <input type="file" onChange={(event) => setFile(event.target.files?.[0] ?? null)} required />
      </label>
      <button type="submit">Create sheet</button>
    </form>
  );
}

function Sheet({
  job,
  onChange,
  onError,
}: {
  job: Job;
  onChange: (job: Job) => void;
  onError: (message: string) => void;
}) {
  async function start() {
    onError("");
    try {
      const next = await api<Job>("/api/jobs/" + job.id + "/start", { method: "POST" });
      onChange(next);
    } catch (err) {
      onError(err instanceof Error ? err.message : "Could not start the sheet");
    }
  }

  async function attach(file: File | undefined) {
    if (!file) {
      return;
    }
    const tooBig = checkSize(file);
    if (tooBig) {
      onError(tooBig);
      return;
    }
    onError("");
    try {
      onChange(await upload(job.id, file));
    } catch (err) {
      onError(err instanceof Error ? err.message : "Could not attach the brochure");
    }
  }

  return (
    <article className="sheet">
      <h2>{job.title}</h2>
      <p>{job.address}</p>
      <p className={"status " + job.status}>{job.status}</p>
      {job.filename ? <p className="muted">{job.filename}</p> : null}
      {job.error ? <p className="error">{job.error}</p> : null}
      <ol>
        {job.checklist.map((step) => (
          <li key={step.label} className={step.state}>
            {step.label}
          </li>
        ))}
      </ol>
      {job.status === "draft" && !job.filename ? (
        <label>
          Brochure
          <input type="file" onChange={(event) => attach(event.target.files?.[0])} />
        </label>
      ) : null}
      {job.status === "draft" && job.filename ? <button onClick={start}>Start</button> : null}
    </article>
  );
}
