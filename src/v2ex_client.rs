use reqwest::{Client,Error};
use crate::models::Topic;

pub async fn fetch_latest_topics(client: &Client) -> Result<Vec<Topic>, Error> {
    let url = "https://www.v2ex.com/api/topics/latest.json";
    let response = client.get(url).send().await?.json::<Vec<Topic>>().await?;
    Ok(response)
}

pub async fn fetch_hot_topics(client: &Client) -> Result<Vec<Topic>, Error> {
    let url = "https://www.v2ex.com/api/topics/hot.json";
    let response = client.get(url).send().await?.json::<Vec<Topic>>().await?;
    Ok(response)
}
