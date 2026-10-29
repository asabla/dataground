import { type ReactNode, useEffect, useState } from "react";
import { Tabs as AriaTabs, Tab, TabList, TabPanel } from "react-aria-components";

export interface TabItem {
  id: string;
  label: string;
  content: ReactNode;
  isDisabled?: boolean;
}

export interface TabsProps {
  label: string;
  items: readonly TabItem[];
  selectedId: string;
  onSelectionChange: (id: string) => void;
}

// Visit panels lazily, then retain their children. Changing the selected view
// must not destroy a consumer's pending command or unfinished form.
export function Tabs({ label, items, selectedId, onSelectionChange }: TabsProps) {
  const [visited, setVisited] = useState(() => new Set([selectedId]));
  useEffect(() => {
    setVisited((current) =>
      current.has(selectedId) ? current : new Set([...current, selectedId]),
    );
  }, [selectedId]);
  return (
    <AriaTabs
      className="dg-tabs"
      selectedKey={selectedId}
      keyboardActivation="manual"
      onSelectionChange={(key) => {
        if (typeof key === "string") onSelectionChange(key);
      }}
    >
      <TabList aria-label={label} className="dg-tabs__list">
        {items.map((item) => (
          <Tab key={item.id} id={item.id} isDisabled={item.isDisabled} className="dg-tabs__tab">
            {item.label}
          </Tab>
        ))}
      </TabList>
      {items.map((item) => (
        <TabPanel key={item.id} id={item.id} shouldForceMount className="dg-tabs__panel">
          {selectedId === item.id || visited.has(item.id) ? item.content : null}
        </TabPanel>
      ))}
    </AriaTabs>
  );
}
