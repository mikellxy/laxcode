import "./logo.css";

export function Logo({ className = "" }: { className?: string }) {
  return <span className={`laxcode-logo ${className}`} role="img" aria-label="LaxCode">
    <span className="laxcode-logo-orbit" aria-hidden="true" />
    <span className="laxcode-logo-word" aria-hidden="true">LaxCode</span>
  </span>;
}
