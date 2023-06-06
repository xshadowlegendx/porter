import React, { useEffect, useState, useContext } from "react";
import styled from "styled-components";

import api from "shared/api";
import { Context } from "shared/Context";

import Text from "components/porter/Text";
import Container from "components/porter/Container";

import EventCard from "./events/EventCard";
import Loading from "components/Loading";
import Spacer from "components/porter/Spacer";
import Fieldset from "components/porter/Fieldset";

import { feedDate } from "shared/string_utils";
import Pagination from "components/porter/Pagination";
import { PorterAppEvent, PorterAppEventType } from "shared/types";

type Props = {
  chart: any;
  stackName: string;
  appData: string;
};

const ActivityFeed: React.FC<Props> = ({ chart, stackName, appData }) => {
  const { currentProject, currentCluster } = useContext(Context);

  const [events, setEvents] = useState<any[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<any>(null);
  const [page, setPage] = useState<number>(1);
  const [numPages, setNumPages] = useState<number>(0);

  const getEvents = async () => {
    setLoading(true);
    try {
      const res = await api.getFeedEvents(
        "<token>",
        {},
        {
          cluster_id: currentCluster.id,
          project_id: currentProject.id,
          stack_name: stackName,
          page,
        }
      );
      setNumPages(res.data.num_pages);
      setEvents(res.data.events);
      setLoading(false);
    } catch (err) {
      setError(err);
      setLoading(false);
    }
  };

  useEffect(() => {
    getEvents();
  }, [page]);

  if (error) {
    return (
      <Fieldset>
        <Text size={16}>Error retrieving events</Text>
        <Spacer height="15px" />
        <Text color="helper">An unexpected error occurred.</Text>
      </Fieldset>
    );
  }

  if (loading) {
    return (
      <div>
        <Spacer y={2} />
        <Loading />
      </div>
    );
  }

  if (events?.length === 0) {
    return (
      <Fieldset>
        <Text size={16}>No events found for "{stackName}"</Text>
        <Spacer height="15px" />
        <Text color="helper">
          This application currently has no associated events.
        </Text>
      </Fieldset>
    );
  }

  return (
    <StyledActivityFeed>
      {events.map((event, i) => {
        return (
          <EventWrapper isLast={i === events.length - 1} key={i}>
            {i !== events.length - 1 && events.length > 1 && <Line />}
            <Dot />
            <Time>
              <Text>{feedDate(event.created_at).split(", ")[0]}</Text>
              <Spacer x={0.5} />
              <Text>{feedDate(event.created_at).split(", ")[1]}</Text>
            </Time>
            <EventCard appData={appData} event={event} key={i} />
          </EventWrapper>
        );
      })}
      <Spacer y={1} />
      {numPages > 1 && (
        <Pagination page={page} setPage={setPage} totalPages={numPages} />
      )}
    </StyledActivityFeed>
  );
};

export default ActivityFeed;

const Time = styled.div`
  opacity: 0;
  animation: fadeIn 0.3s 0.1s;
  animation-fill-mode: forwards;
  width: 90px;
`;

const Line = styled.div`
  width: 1px;
  height: calc(100% + 30px);
  background: #414141;
  position: absolute;
  left: 3px;
  top: 36px;
  opacity: 0;
  animation: fadeIn 0.3s 0.1s;
  animation-fill-mode: forwards;
`;

const Dot = styled.div`
  width: 7px;
  height: 7px;
  background: #fff;
  border-radius: 50%;
  position: absolute;
  left: 0;
  top: 36px;
  opacity: 0;
  animation: fadeIn 0.3s 0.1s;
  animation-fill-mode: forwards;
`;

const EventWrapper = styled.div<{
  isLast: boolean;
}>`
  padding-left: 30px;
  display: flex;
  align-items: center;
  position: relative;
  margin-bottom: ${(props) => (props.isLast ? "" : "25px")};
`;

const StyledActivityFeed = styled.div`
  width: 100%;
  animation: fadeIn 0.3s 0s;
  @keyframes fadeIn {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }
`;