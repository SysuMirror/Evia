import { useId, type CSSProperties, type ReactNode } from 'react';
import { LoadingPanel } from './layouts';

export interface DataTableColumn<T> {
  key: string;
  header: ReactNode;
  render: (row: T) => ReactNode;
  width?: CSSProperties['width'];
  align?: 'left' | 'center' | 'right';
}

export interface DataTableProps<T> {
  caption: string;
  columns: readonly DataTableColumn<T>[];
  rows: readonly T[];
  rowKey: (row: T) => string;
  empty?: ReactNode;
  loading?: boolean;
  minWidth?: CSSProperties['minWidth'];
}

export function DataTable<T>({
  caption,
  columns,
  rows,
  rowKey,
  empty = '暂无数据',
  loading = false,
  minWidth = 600,
}: DataTableProps<T>) {
  const captionID = useId();
  return (
    <div
      className="evia-table-region"
      role="region"
      aria-labelledby={captionID}
      aria-busy={loading}
      tabIndex={0}
    >
      <table className="evia-table" style={{ minWidth }}>
        <caption id={captionID} className="sr-only">
          {caption}
        </caption>
        <thead>
          <tr>
            {columns.map((column) => (
              <th
                key={column.key}
                scope="col"
                style={{ width: column.width, textAlign: column.align }}
              >
                {column.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={rowKey(row)}>
              {columns.map((column) => (
                <td key={column.key} style={{ textAlign: column.align }}>
                  {column.render(row)}
                </td>
              ))}
            </tr>
          ))}
          {rows.length === 0 && (
            <tr>
              <td colSpan={columns.length} className="evia-table-empty">
                {loading ? <LoadingPanel /> : empty}
              </td>
            </tr>
          )}
        </tbody>
      </table>
      {loading && rows.length > 0 && (
        <span className="sr-only" role="status">
          正在刷新
        </span>
      )}
    </div>
  );
}
