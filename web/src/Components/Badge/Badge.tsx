export function Badge(props: { status: string }) {
  return (
    <div className="badge">
      {props.status}
    </div>
  );
}
