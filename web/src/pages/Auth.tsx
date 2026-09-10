import { Link, useNavigate } from "react-router-dom";
import { AuthorizeForm } from "../components/AuthorizeForm";
import { IconBack } from "../components/Icons";
import { useStatus } from "../components/StatusContext";
import { useToast } from "../components/Toast";

export function AuthPage() {
  const { status, refresh } = useStatus();
  const toast = useToast();
  const navigate = useNavigate();

  return (
    <div className="stack" style={{ maxWidth: 600 }}>
      <div>
        <Link to="/" className="back">
          <IconBack /> Dashboard
        </Link>
      </div>
      <div>
        <h1 className="page-title">Re-authorize with GOG</h1>
        {status?.auth_error && (
          <p className="summary err-text">The stored GOG session stopped working: {status.auth_error}</p>
        )}
      </div>
      <AuthorizeForm
        currentUser={status?.authenticated ? status.user : null}
        onConnected={(u) => {
          toast.success(`Connected as ${u.username}`);
          refresh();
          navigate("/");
        }}
      />
    </div>
  );
}
