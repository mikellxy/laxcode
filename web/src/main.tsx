import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./app/App";
import { Providers } from "./app/providers";
import "./index.css";
import { lazy, Suspense } from "react";
import { isAdminPath } from "./admin/api";

const AdminApp = lazy(() => import("./admin/AdminApp").then((module) => ({ default: module.AdminApp })));

createRoot(document.getElementById("root")!).render(<StrictMode>{isAdminPath(location.pathname)
  ? <Suspense fallback={<div role="status">正在加载管理后台…</div>}><AdminApp /></Suspense>
  : <Providers><App /></Providers>}</StrictMode>);
